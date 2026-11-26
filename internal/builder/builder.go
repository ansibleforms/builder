// Package builder runs the builds: one at a time, handed over the socket. Each one's files are
// written, built with BuildKit and pushed with the registry's login, signed with the team's
// cosign key when asked ; its output streamed back as the job's, through a spool: what the
// controller did not ack is sent again after a reconnect.
package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ansibleforms/builder/internal/buildkit"
	"github.com/ansibleforms/builder/internal/logger"
	"github.com/ansibleforms/builder/internal/registry"
	"github.com/ansibleforms/builder/internal/settings"
	"github.com/ansibleforms/builder/internal/shutdown"
	"github.com/ansibleforms/builder/internal/sign"
	"github.com/ansibleforms/builder/internal/socket"
	"github.com/ansibleforms/builder/internal/version"
)

// ─── state ────────────────────────────────────────────────────────────────────

// What a build calls, the tests replace: BuildKit, the signature, the stopping process, and the
// two file operations that cannot fail in a fresh temporary folder.
var (
	buildablePlatforms = buildkit.BuildablePlatforms
	buildRun           = buildkit.Run
	loadKey            = sign.LoadKey
	signImage          = sign.Image
	isStopping         = shutdown.IsStopping
	mkdir              = os.Mkdir
	writeDockerConfig  = registry.WriteDockerConfig
)

// fileName is the name a build's file may have.
var fileName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// job is one build the builder holds: running, or ended with items the controller has not
// acked yet.
type job struct {
	spool   *socket.Spool
	running bool
	// cancel stops its BuildKit build.
	cancel  context.CancelFunc
	aborted bool
	// order numbers its output lines, after the ones the controller has already.
	order int64
	// secrets are the values masked in its output.
	secrets []string
}

// Builder is the builder's state: its socket to the controller and the builds it holds.
type Builder struct {
	mu       sync.Mutex
	session  *socket.Session
	welcomed bool
	jobs     map[int64]*job
}

// New makes a builder holding no build.
//
// Returns:
//
//	The builder.
func New() *Builder {
	return &Builder{jobs: map[int64]*job{}}
}

// spoolDir is the folder of the builds' spools.
//
// Returns:
//
//	The folder.
func spoolDir() string { return filepath.Join(settings.WorkDir(), "spool") }

// capacity says what it can take: one build at a time. The caller holds the lock.
//
// Returns:
//
//	{ max, running, free }.
func (b *Builder) capacity() map[string]int {
	running := len(b.running())
	return map[string]int{"max": 1, "running": running, "free": max(0, 1-running)}
}

// running lists the builds it runs. The caller holds the lock.
//
// Returns:
//
//	Their jobs.
func (b *Builder) running() []int64 {
	ids := []int64{}
	for id, j := range b.jobs {
		if j.running {
			ids = append(ids, id)
		}
	}
	return ids
}

// ─── items to the controller ──────────────────────────────────────────────────

// pump sends a build's items not sent yet, within the window of items not acked. The caller
// holds the lock.
//
// Args:
//
//	jobID: the build's job.
func (b *Builder) pump(jobID int64) {
	j := b.jobs[jobID]
	if j == nil || b.session == nil || !b.welcomed {
		return
	}
	sp := j.spool
	for sp.Next <= sp.Seq && sp.Next-sp.Acked <= socket.Window {
		item := sp.Get(sp.Next)
		sp.Next++
		if item != nil && !b.session.Send(socket.Message{"t": "item", "jobId": jobID, "item": item}) {
			sp.Rewind()
			return
		}
	}
}

// emit adds an item to a build's spool and sends it. The caller holds the lock.
//
// Args:
//
//	jobID: the build's job.
//	item: { kind, ... }.
func (b *Builder) emit(jobID int64, item socket.Item) {
	j := b.jobs[jobID]
	if j == nil {
		return
	}
	j.spool.Append(item)
	b.pump(jobID)
}

// say sends a line of the build's output, masked of its secrets.
//
// Args:
//
//	jobID: the build's job.
//	text: the line(s).
//	stream: stdout or stderr.
func (b *Builder) say(jobID int64, text, stream string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[jobID]
	if j == nil || text == "" {
		return
	}
	for _, s := range j.secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, "********")
		}
	}
	j.order++
	b.emit(jobID, socket.Item{"kind": "output", "record": map[string]any{"output": text, "output_type": stream, "order": j.order}})
}

// lineWriter hands what a build writes to say, whole lines at a time.
type lineWriter struct {
	b     *Builder
	jobID int64
	rest  string
}

// Write says the whole lines written so far, keeping a cut last line for the next write.
//
// Args:
//
//	p: what was written.
//
// Returns:
//
//	len(p), and no error.
func (w *lineWriter) Write(p []byte) (int, error) {
	text := strings.ReplaceAll(w.rest+string(p), "\r\n", "\n")
	parts := strings.Split(text, "\n")
	w.rest = parts[len(parts)-1]
	if len(parts) > 1 {
		w.b.say(w.jobID, strings.Join(parts[:len(parts)-1], "\n"), "stdout")
	}
	return len(p), nil
}

// flush says a last line written without its newline.
func (w *lineWriter) flush() {
	if w.rest != "" {
		w.b.say(w.jobID, w.rest, "stdout")
		w.rest = ""
	}
}

// ─── building ─────────────────────────────────────────────────────────────────

// spec is a build as the controller hands it over.
type spec struct {
	// Files are the build's files by name, its Dockerfile among them.
	Files map[string]any `json:"files"`
	// Ref is the reference to push to.
	Ref string `json:"ref"`
	// Arch is amd64, arm64 or all ; none : the builder's own.
	Arch any `json:"arch"`
	// SBOM and Sign are truthy values (a controller may send 1 or true).
	SBOM any `json:"sbom"`
	Sign any `json:"sign"`
}

// secrets are a build's secrets, sealed for this builder.
type secrets struct {
	Registry *registry.Login `json:"registry"`
	Signing  *struct {
		Key      string `json:"key"`
		Password string `json:"password"`
	} `json:"signing"`
}

// truthy reads a JSON value as JavaScript does in a condition.
//
// Args:
//
//	v: the value.
//
// Returns:
//
//	False for nil, false, 0 and "", true otherwise.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

// text reads a build file's content as text.
//
// Args:
//
//	v: the value.
//
// Returns:
//
//	The text.
func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// runBuild builds an image and pushes it ; signs it when asked. Ends the build's job.
//
// Args:
//
//	ctx: the build's context, cancelled by an abort.
//	jobID: the build's job.
//	s: the build.
//	sec: its secrets.
func (b *Builder) runBuild(ctx context.Context, jobID int64, s spec, sec secrets) {
	status, message := "failed", ""
	artifacts := map[string]any{"ref": s.Ref, "sbom": truthy(s.SBOM), "signed": false}
	dir, err := os.MkdirTemp(settings.WorkDir(), fmt.Sprintf("build-%d-", jobID))
	defer func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		b.mu.Lock()
		if j := b.jobs[jobID]; j != nil {
			j.running = false
			j.cancel()
			j.order++
			stream := "stderr"
			if status == "success" {
				stream = "stdout"
			}
			// its free slot first, then its end : nothing is read after the end is sent
			b.sendHealth()
			b.emit(jobID, socket.Item{"kind": "end", "status": status, "order": j.order, "stream": stream, "message": message, "artifacts": artifacts})
		}
		b.mu.Unlock()
	}()
	if err != nil {
		dir = ""
		message = fmt.Sprintf("the build failed: %v", err)
		return
	}
	name := settings.BuilderName()

	// step 1 : the build's files, and the registry's login
	ctxDir := filepath.Join(dir, "context")
	if err := mkdir(ctxDir, 0o700); err != nil {
		message = fmt.Sprintf("the build failed: %v", err)
		return
	}
	for file, content := range s.Files {
		if !fileName.MatchString(file) {
			message = fmt.Sprintf("the build failed: '%s' is no file name of a build", file)
			return
		}
		if err := os.WriteFile(filepath.Join(ctxDir, file), []byte(text(content)), 0o644); err != nil {
			message = fmt.Sprintf("the build failed: %v", err)
			return
		}
	}
	conf, err := writeDockerConfig(dir, sec.Registry)
	if err != nil {
		message = fmt.Sprintf("the build failed: %v", err)
		return
	}
	// its architectures : another one than the host's is built only when QEMU is registered on
	// the host, so a missing one stops the build here rather than push a partial image
	arch, _ := s.Arch.(string)
	platforms, err := buildkit.PlatformsOf(arch)
	if err != nil {
		message = fmt.Sprintf("the build failed: %v", err)
		return
	}
	if len(platforms) > 0 {
		if can := buildablePlatforms(); can != nil {
			var missing []string
			for _, p := range platforms {
				if !contains(can, p) {
					missing = append(missing, p)
				}
			}
			if len(missing) > 0 {
				message = fmt.Sprintf("builder '%s' cannot build %s: run once on its host %s", name, strings.Join(missing, ", "), buildkit.QEMUHint)
				b.say(jobID, message, "stderr")
				return
			}
		}
	}
	on := ""
	if len(platforms) > 0 {
		on = " for " + strings.Join(platforms, ", ")
	}
	b.say(jobID, fmt.Sprintf("Building %s%s on builder '%s'", s.Ref, on, name), "stdout")

	// step 2 : the build, pushed ; its digest from BuildKit's answer
	w := &lineWriter{b: b, jobID: jobID}
	digest, err := buildRun(ctx, buildkit.Build{
		Context: ctxDir, DockerConfig: conf, Ref: s.Ref, Platforms: platforms,
		SBOM: truthy(s.SBOM), Insecure: registry.Insecure(sec.Registry),
	}, w)
	w.flush()
	if b.wasAborted(jobID) {
		status, message = "aborted", "the build was aborted"
		return
	}
	if err != nil {
		message = fmt.Sprintf("the build failed: %v", err)
		b.say(jobID, message, "stderr")
		return
	}
	artifacts["digest"] = digest
	at := ""
	if digest != "" {
		at = "@" + digest
	}
	b.say(jobID, fmt.Sprintf("Pushed %s%s", s.Ref, at), "stdout")

	// step 3 : signed with the team's key, offline (no public transparency log), as cosign 3 signs
	// with --use-signing-config=false --tlog-upload=false (internal/sign)
	if truthy(s.Sign) && sec.Signing != nil && sec.Signing.Key != "" {
		signer, err := loadKey(sec.Signing.Key, sec.Signing.Password)
		if err != nil {
			message = fmt.Sprintf("the image was pushed but could not be signed: %v", err)
			b.say(jobID, message, "stderr")
			return
		}
		signed, err := signImage(s.Ref, digest, signer, sec.Registry)
		if err != nil {
			message = fmt.Sprintf("the image was pushed but could not be signed: %v", err)
			b.say(jobID, message, "stderr")
			return
		}
		artifacts["signed"] = true
		b.say(jobID, "Signed "+signed, "stdout")
	}
	status, message = "success", fmt.Sprintf("RTE image %s built and pushed", s.Ref)
}

// contains tells whether a list holds a value.
//
// Args:
//
//	list: the list.
//	v: the value.
//
// Returns:
//
//	True when it is in it.
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// wasAborted tells whether a build was cancelled.
//
// Args:
//
//	jobID: the build's job.
//
// Returns:
//
//	True once the controller cancelled it.
func (b *Builder) wasAborted(jobID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[jobID]
	return j != nil && j.aborted
}

// ─── the controller's messages ────────────────────────────────────────────────

// jobIDOf reads a message's job id, a number or its text.
//
// Args:
//
//	v: the value.
//
// Returns:
//
//	The id, 0 when it is none.
func jobIDOf(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		// as parseInt : the digits it starts with
		end := 0
		for end < len(x) && x[end] >= '0' && x[end] <= '9' {
			end++
		}
		id, _ := strconv.ParseInt(x[:end], 10, 64)
		return id
	}
	return 0
}

// onRun takes a build handed over: claimed (or refused when busy), its secrets opened, run.
// The caller holds the lock.
//
// Args:
//
//	s: the socket.
//	msg: { jobId, contract, sealed, job: { lastOrder }, build }.
func (b *Builder) onRun(s *socket.Session, msg socket.Message) {
	jobID := jobIDOf(msg["jobId"])
	refuse := func(why string, busy bool) {
		m := socket.Message{"t": "refused", "jobId": jobID, "error": why}
		if busy {
			m["busy"] = true
		}
		s.Send(m)
	}
	if c, _ := msg["contract"].(float64); c != socket.SocketContract {
		refuse(fmt.Sprintf("this builder speaks contract %d", socket.SocketContract), false)
		return
	}
	if msg["build"] == nil {
		refuse("this is a builder: it builds RTE images, it runs no playbook", false)
		return
	}
	if isStopping() {
		refuse("this builder is stopping", true)
		return
	}
	if j := b.jobs[jobID]; j != nil && j.running {
		s.Send(socket.Message{"t": "claimed", "jobId": jobID})
		return
	}
	if b.capacity()["free"] == 0 {
		refuse("this builder runs a build already", true)
		return
	}
	var sec secrets
	if err := socket.OpenJobSecrets(msg["sealed"], settings.Token(), jobID, &sec); err != nil {
		refuse("the build's secrets do not open: "+err.Error(), false)
		return
	}
	var sp spec
	// what was read from JSON writes back as JSON : only its shape can be wrong
	raw, _ := json.Marshal(msg["build"])
	if json.Unmarshal(raw, &sp) != nil {
		refuse("the build cannot be read", false)
		return
	}
	var masked []string
	if sec.Registry != nil {
		masked = append(masked, sec.Registry.Password)
	}
	if sec.Signing != nil {
		masked = append(masked, sec.Signing.Key, sec.Signing.Password)
	}
	var lastOrder float64
	if jm, ok := msg["job"].(map[string]any); ok {
		lastOrder, _ = jm["lastOrder"].(float64)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.jobs[jobID] = &job{spool: socket.OpenSpool(spoolDir(), jobID), running: true, cancel: cancel, order: int64(lastOrder), secrets: masked}
	s.Send(socket.Message{"t": "claimed", "jobId": jobID})
	b.emit(jobID, socket.Item{"kind": "started", "pid": os.Getpid()})
	b.sendHealth()
	go b.runBuild(ctx, jobID, sp, sec)
}

// onCancel stops a build: its BuildKit build cancelled. The caller holds the lock.
//
// Args:
//
//	msg: { jobId }.
func (b *Builder) onCancel(msg socket.Message) {
	j := b.jobs[jobIDOf(msg["jobId"])]
	if j == nil || !j.running {
		return
	}
	j.aborted = true
	j.cancel()
}

// onAck takes the controller's word that it stored a build's items up to seq: an ended build's
// spool goes. The caller holds the lock.
//
// Args:
//
//	msg: { jobId, seq }.
func (b *Builder) onAck(msg socket.Message) {
	jobID := jobIDOf(msg["jobId"])
	j := b.jobs[jobID]
	if j == nil {
		return
	}
	seq, _ := msg["seq"].(float64)
	if j.spool.Ack(int64(seq)) && !j.running {
		j.spool.Remove()
		delete(b.jobs, jobID)
		return
	}
	b.pump(jobID)
}

// SendHealth sends the builder's health to the controller: what it builds, its free slot.
func (b *Builder) SendHealth() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sendHealth()
}

// sendHealth sends the health. The caller holds the lock.
func (b *Builder) sendHealth() {
	if b.welcomed && b.session != nil {
		b.session.Send(socket.Message{"t": "health", "capacity": b.capacity(), "running": b.running(), "stopping": isStopping()})
	}
}

// ─── the socket ───────────────────────────────────────────────────────────────

// Attach takes a socket to the controller: says hello, and once welcomed sends what is not acked
// yet.
//
// Args:
//
//	s: the socket.
func (b *Builder) Attach(s *socket.Session) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != nil && b.session != s {
		b.session.Close(socket.CloseReplaced, "a newer socket took over")
	}
	b.session = s
	b.welcomed = false
	spooled := []int64{}
	for id := range b.jobs {
		spooled = append(spooled, id)
	}
	s.Send(socket.Message{
		"t":        "hello",
		"id":       settings.NodeID(),
		"name":     settings.BuilderName(),
		"version":  version.App,
		"contract": socket.SocketContract,
		"mode":     "outbound",
		"kind":     "builder",
		"capacity": b.capacity(),
		"running":  b.running(),
		"spooled":  spooled,
	})
}

// Handle takes a message of the controller.
//
// Args:
//
//	s: the socket it came on.
//	msg: the message.
func (b *Builder) Handle(s *socket.Session, msg socket.Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch msg.Type() {
	case "welcome":
		b.welcomed = true
		node, _ := msg["node"].(string)
		if node == "" {
			node = "?"
		}
		logger.Notice("Builder '%s': connected to the controller (node %s)", settings.BuilderName(), node)
		if truthy(msg["pending"]) {
			logger.Warning("Builder '%s': waiting for an admin to approve it in the Designer's RTE builders", settings.BuilderName())
		}
		for id := range b.jobs {
			b.pump(id)
		}
	case "run":
		b.onRun(s, msg)
	case "cancel":
		b.onCancel(msg)
	case "ack":
		b.onAck(msg)
	}
}

// Detach lets go of a socket that is gone.
//
// Args:
//
//	s: the socket.
func (b *Builder) Detach(s *socket.Session) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == s {
		b.session = nil
		b.welcomed = false
	}
}

// ResumeSpools takes the builds an earlier start left unfinished: their spools kept to be sent
// again, those not ended said abandoned (the builder restarted while it built).
//
// Errors:
//
//	When the spool folder cannot be made or read.
func (b *Builder) ResumeSpools() error {
	dir := spoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range entries {
		id := socket.SpoolJobID(e.Name())
		if id == 0 {
			continue
		}
		sp := socket.OpenSpool(dir, id)
		b.jobs[id] = &job{spool: sp, cancel: func() {}}
		if !sp.Ended {
			sp.Append(socket.Item{"kind": "abandon", "message": "the builder restarted while it built"})
		}
	}
	return nil
}

// IsWelcomed tells whether the controller welcomed this builder (its /ready).
//
// Returns:
//
//	True while welcomed.
func (b *Builder) IsWelcomed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.welcomed
}
