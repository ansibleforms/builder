package builder

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ansibleforms/builder/internal/buildkit"
	"github.com/ansibleforms/builder/internal/registry"
	"github.com/ansibleforms/builder/internal/socket"
)

// ─── a fake controller ────────────────────────────────────────────────────────

// controller is a fake controller: it takes the builder's socket, welcomes it, and hands what
// the builder sends to the test.
type controller struct {
	server *httptest.Server
	conn   chan *websocket.Conn
	got    chan socket.Message
}

// newController starts a fake controller at /api/v2/rte/socket.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The controller ; CONTROLLER_URL is set to it.
func newController(t *testing.T) *controller {
	c := &controller{conn: make(chan *websocket.Conn, 1), got: make(chan socket.Message, 1000)}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != socket.AppSocketPath || r.Header.Get("X-AF-Peer-Kind") != "builder" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.conn <- conn
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			if m := socket.Decode(data); m != nil {
				c.got <- m
			}
		}
	}))
	t.Cleanup(c.server.Close)
	t.Setenv("CONTROLLER_URL", c.server.URL)
	return c
}

// send sends a message to the builder.
//
// Args:
//
//	t: the test.
//	conn: the builder's socket.
//	msg: the message.
func send(t *testing.T, conn *websocket.Conn, msg socket.Message) {
	data, _ := json.Marshal(msg)
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

// next waits for the builder's next message of a type, the others skipped.
//
// Args:
//
//	t: the test.
//	c: the controller.
//	want: the type.
//	timeout: how long to wait.
//
// Returns:
//
//	The message.
func (c *controller) next(t *testing.T, want string, timeout time.Duration) socket.Message {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m := <-c.got:
			if m.Type() == want {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s message in %s", want, timeout)
		}
	}
}

// itemEnd waits for a job's end item, and collects its output on the way.
//
// Args:
//
//	t: the test.
//	c: the controller.
//	timeout: how long to wait.
//
// Returns:
//
//	The end item, the output, and the last item's seq.
func (c *controller) itemEnd(t *testing.T, timeout time.Duration) (map[string]any, string, float64) {
	t.Helper()
	var out strings.Builder
	for {
		m := c.next(t, "item", timeout)
		item := m["item"].(map[string]any)
		switch item["kind"] {
		case "output":
			out.WriteString(item["record"].(map[string]any)["output"].(string) + "\n")
		case "end":
			return item, out.String(), item["seq"].(float64)
		}
	}
}

// ─── the tests ────────────────────────────────────────────────────────────────

// start sets the builder's environment and dials the fake controller.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The controller, the builder's socket on its side, and the builder.
func start(t *testing.T) (*controller, *websocket.Conn, *Builder) {
	t.Setenv("RTE_TOKEN", "token-0123456789abcdef")
	t.Setenv("RTE_NAME", "builder-test")
	t.Setenv("BUILDER_WORK_PATH", t.TempDir())
	c := newController(t)
	b := New()
	if err := b.ResumeSpools(); err != nil {
		t.Fatal(err)
	}
	d, err := socket.DialOut("token-0123456789abcdef", b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	conn := <-c.conn
	hello := c.next(t, "hello", 5*time.Second)
	if hello["kind"] != "builder" || hello["contract"].(float64) != socket.SocketContract {
		t.Fatalf("hello %v", hello)
	}
	send(t, conn, socket.Message{"t": "welcome", "node": "app-1"})
	return c, conn, b
}

// run hands a build over, its secrets sealed for the builder.
//
// Args:
//
//	t: the test.
//	conn: the builder's socket.
//	jobID: the job.
//	build: the build.
//	secrets: its secrets.
func run(t *testing.T, conn *websocket.Conn, jobID int64, build map[string]any, secrets any) {
	sealed, err := socket.SealJobSecrets(secrets, "token-0123456789abcdef", jobID)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, socket.Message{"t": "run", "jobId": jobID, "contract": socket.SocketContract, "sealed": sealed, "job": map[string]any{"lastOrder": 10}, "build": build})
}

// TestRefusals checks that a playbook job, another contract and secrets for another builder are
// refused.
func TestRefusals(t *testing.T) {
	c, conn, _ := start(t)
	send(t, conn, socket.Message{"t": "run", "jobId": 1, "contract": socket.SocketContract})
	if m := c.next(t, "refused", 5*time.Second); !strings.Contains(m["error"].(string), "runs no playbook") {
		t.Errorf("a playbook: %v", m)
	}
	send(t, conn, socket.Message{"t": "run", "jobId": 2, "contract": 99, "build": map[string]any{}})
	if m := c.next(t, "refused", 5*time.Second); !strings.Contains(m["error"].(string), "contract") {
		t.Errorf("another contract: %v", m)
	}
	sealed, _ := socket.SealJobSecrets(map[string]any{}, "another-token-0123456", 3)
	send(t, conn, socket.Message{"t": "run", "jobId": 3, "contract": socket.SocketContract, "sealed": sealed, "build": map[string]any{}})
	if m := c.next(t, "refused", 5*time.Second); !strings.Contains(m["error"].(string), "do not open") {
		t.Errorf("another token: %v", m)
	}
}

// TestFailedBuild runs a build against a BuildKit that is not there: claimed, started, its end
// failed with the reason, its secrets masked, and its spool gone once the end is acked.
func TestFailedBuild(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "tcp://127.0.0.1:1")
	c, conn, _ := start(t)
	run(t, conn, 7, map[string]any{"ref": "registry.example.com/af/rte:1", "files": map[string]any{"Dockerfile": "FROM scratch\n"}},
		map[string]any{"registry": map[string]any{"url": "https://registry.example.com", "user": "u", "password": "s3cret-pass"}})
	if m := c.next(t, "claimed", 5*time.Second); m["jobId"].(float64) != 7 {
		t.Errorf("claimed %v", m)
	}
	end, out, seq := c.itemEnd(t, 30*time.Second)
	if end["status"] != "failed" || !strings.Contains(end["message"].(string), "the build failed") {
		t.Errorf("end %v", end)
	}
	if !strings.Contains(out, "Building registry.example.com/af/rte:1 on builder 'builder-test'") || strings.Contains(out, "s3cret-pass") {
		t.Errorf("output %q", out)
	}
	spool := socket.SpoolFile(spoolDir(), 7)
	if _, err := os.Stat(spool); err != nil {
		t.Fatal("no spool before the ack")
	}
	send(t, conn, socket.Message{"t": "ack", "jobId": 7, "seq": seq})
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(spool); os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("the spool stayed after its end was acked")
}

// TestBadFileName checks that a file name with a path is refused before anything is built.
func TestBadFileName(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "tcp://127.0.0.1:1")
	c, conn, _ := start(t)
	run(t, conn, 8, map[string]any{"ref": "r:1", "files": map[string]any{"../x": "y"}}, map[string]any{})
	end, _, _ := c.itemEnd(t, 10*time.Second)
	if !strings.Contains(end["message"].(string), "no file name of a build") {
		t.Errorf("end %v", end)
	}
}

// TestRealBuild builds and pushes an image with a real BuildKit, when E2E_BUILDKIT_HOST names
// one and E2E_REGISTRY a registry it pushes to over plain http (host:port). With E2E_COSIGN, a
// cosign binary, it also signs the image with a key cosign made, and cosign verify checks it.
func TestRealBuild(t *testing.T) {
	host, reg, cosign := os.Getenv("E2E_BUILDKIT_HOST"), os.Getenv("E2E_REGISTRY"), os.Getenv("E2E_COSIGN")
	if host == "" || reg == "" {
		t.Skip("E2E_BUILDKIT_HOST and E2E_REGISTRY are not set")
	}
	t.Setenv("BUILDKIT_HOST", host)
	secrets := map[string]any{"registry": map[string]any{"url": "http://" + reg, "user": "u", "password": "e2e-registry-pass"}}
	keyDir := t.TempDir()
	if cosign != "" {
		// step 1 : a key pair as a team makes it
		cmd := exec.Command(cosign, "generate-key-pair")
		cmd.Dir = keyDir
		cmd.Env = append(os.Environ(), "COSIGN_PASSWORD=e2e-key-pass")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cosign generate-key-pair: %v\n%s", err, out)
		}
		key, err := os.ReadFile(filepath.Join(keyDir, "cosign.key"))
		if err != nil {
			t.Fatal(err)
		}
		secrets["signing"] = map[string]any{"key": string(key), "password": "e2e-key-pass"}
	}

	// step 2 : the build, pushed and signed
	// a repository and a file of its own each run : nothing cached, no signature of before
	c, conn, _ := start(t)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	repo := reg + "/af/rte-test-" + stamp
	ref := repo + ":1"
	run(t, conn, 9, map[string]any{"ref": ref, "arch": "amd64", "sbom": false, "sign": cosign != "", "files": map[string]any{
		"Dockerfile": "FROM busybox\nCOPY hello.txt /hello.txt\nRUN cat /hello.txt\n",
		"hello.txt":  "hello from the builder " + stamp,
	}}, secrets)
	end, out, _ := c.itemEnd(t, 5*time.Minute)
	t.Log(out)
	if end["status"] != "success" {
		t.Fatalf("end %v", end)
	}
	art := end["artifacts"].(map[string]any)
	digest, _ := art["digest"].(string)
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("artifacts %v", art)
	}
	if !strings.Contains(out, "hello from the builder "+stamp) || !strings.Contains(out, "Pushed "+ref+"@sha256:") {
		t.Errorf("output %q", out)
	}
	if cosign == "" {
		return
	}

	// step 3 : cosign itself takes the signature
	if art["signed"] != true || !strings.Contains(out, "Signed "+repo+"@"+digest) {
		t.Fatalf("not signed: %v", art)
	}
	cmd := exec.Command(cosign, "verify", "--key", filepath.Join(keyDir, "cosign.pub"), "--insecure-ignore-tlog", "--allow-http-registry", repo+"@"+digest)
	verified, err := cmd.CombinedOutput()
	t.Logf("cosign verify:\n%s", verified)
	if err != nil {
		t.Fatalf("cosign verify refused the signature: %v", err)
	}
}

// ─── builds with BuildKit and the signature faked ────────────────────────────

// fakes replaces what a build calls: BuildKit builds with build, every platform can be built,
// and the signature succeeds.
//
// Args:
//
//	t: the test.
//	build: what the build does ; it writes its progress to out.
func fakes(t *testing.T, build func(ctx context.Context, out io.Writer) (string, error)) {
	t.Helper()
	was := []any{buildablePlatforms, buildRun, loadKey, signImage, isStopping, mkdir, writeDockerConfig}
	buildablePlatforms = func() []string { return []string{"linux/amd64", "linux/arm64"} }
	buildRun = func(ctx context.Context, _ buildkit.Build, out io.Writer) (string, error) { return build(ctx, out) }
	loadKey = func(string, string) (crypto.Signer, error) { return nil, nil }
	signImage = func(ref, digest string, _ crypto.Signer, _ *registry.Login) (string, error) {
		return "r.example.com/af/rte@" + digest, nil
	}
	isStopping = func() bool { return false }
	t.Cleanup(func() {
		buildablePlatforms = was[0].(func() []string)
		buildRun = was[1].(func(context.Context, buildkit.Build, io.Writer) (string, error))
		loadKey = was[2].(func(string, string) (crypto.Signer, error))
		signImage = was[3].(func(string, string, crypto.Signer, *registry.Login) (string, error))
		isStopping = was[4].(func() bool)
		mkdir = was[5].(func(string, os.FileMode) error)
		writeDockerConfig = was[6].(func(string, *registry.Login) (string, error))
	})
}

// pushed is a build that writes some progress, a cut last line included, and pushes.
func pushed(_ context.Context, out io.Writer) (string, error) {
	_, _ = io.WriteString(out, "#1 [1/1] FROM busybox\r\n#1 DONE")
	_, _ = io.WriteString(out, " 0.1s\n#2 exporting")
	return "sha256:" + strings.Repeat("a", 64), nil
}

// signing is a build's secrets with a registry login and a signing key.
var signing = map[string]any{
	"registry": map[string]any{"url": "https://r.example.com", "user": "u", "password": "registry-secret"},
	"signing":  map[string]any{"key": "KEY", "password": "key-secret"},
}

// TestBuildSigned checks a build for both architectures, pushed and signed: its output, cut
// lines joined, and its artifacts.
func TestBuildSigned(t *testing.T) {
	fakes(t, pushed)
	c, conn, _ := start(t)
	run(t, conn, 20, map[string]any{"ref": "r.example.com/af/rte:1", "arch": "all", "sbom": 1, "sign": "yes", "files": map[string]any{"Dockerfile": "FROM busybox\n", "n": 7}}, signing)
	end, out, _ := c.itemEnd(t, 10*time.Second)
	art := end["artifacts"].(map[string]any)
	if end["status"] != "success" || art["signed"] != true || art["sbom"] != true {
		t.Fatalf("end %v", end)
	}
	for _, want := range []string{"for linux/amd64, linux/arm64", "#1 DONE 0.1s", "#2 exporting", "Signed r.example.com/af/rte@sha256:"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
}

// TestBuildOutcomes checks each way a build ends: an architecture the host cannot build, one
// that is none, BuildKit not saying what it builds, a failed build, no digest, a key that does
// not open, a signature refused, and the build's own files and folders failing.
func TestBuildOutcomes(t *testing.T) {
	cases := []struct {
		name  string
		build map[string]any
		setup func()
		want  string
	}{
		{"arm64 missing", map[string]any{"arch": "all"}, func() { buildablePlatforms = func() []string { return []string{"linux/amd64"} } }, "cannot build linux/arm64"},
		{"no architecture", map[string]any{"arch": "s390x"}, nil, "no architecture"},
		{"BuildKit silent", map[string]any{"arch": "amd64"}, func() { buildablePlatforms = func() []string { return nil } }, "built and pushed"},
		{"failed", nil, func() {
			buildRun = func(context.Context, buildkit.Build, io.Writer) (string, error) { return "", errors.New("RUN failed") }
		}, "RUN failed"},
		{"no digest", nil, func() {
			buildRun = func(context.Context, buildkit.Build, io.Writer) (string, error) { return "", nil }
		}, "built and pushed"},
		{"key", map[string]any{"sign": true}, func() {
			loadKey = func(string, string) (crypto.Signer, error) { return nil, errors.New("wrong password") }
		}, "could not be signed: wrong password"},
		{"signature", map[string]any{"sign": true}, func() {
			signImage = func(string, string, crypto.Signer, *registry.Login) (string, error) { return "", errors.New("refused") }
		}, "could not be signed: refused"},
		{"file", map[string]any{"files": map[string]any{".": "x"}}, nil, "the build failed"},
		{"context", nil, func() { mkdir = func(string, os.FileMode) error { return errors.New("no room") } }, "no room"},
		{"login", nil, func() {
			writeDockerConfig = func(string, *registry.Login) (string, error) { return "", errors.New("read-only") }
		}, "read-only"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakes(t, pushed)
			if tc.setup != nil {
				tc.setup()
			}
			c, conn, _ := start(t)
			build := map[string]any{"ref": "r.example.com/af/rte:1", "files": map[string]any{"Dockerfile": "FROM busybox\n"}}
			for k, v := range tc.build {
				build[k] = v
			}
			run(t, conn, int64(30+i), build, signing)
			end, out, _ := c.itemEnd(t, 10*time.Second)
			if !strings.Contains(end["message"].(string), tc.want) {
				t.Errorf("end %v", end)
			}
			if strings.Contains(out, "registry-secret") || strings.Contains(out, "key-secret") {
				t.Errorf("a secret in %q", out)
			}
		})
	}
}

// TestBuildNoWorkFolder checks a build whose temporary folder cannot be made.
func TestBuildNoWorkFolder(t *testing.T) {
	fakes(t, pushed)
	c, conn, _ := start(t)
	// the spool's folder is there already ; the work folder takes nothing new
	_ = os.Chmod(os.Getenv("BUILDER_WORK_PATH"), 0o500)
	t.Cleanup(func() { _ = os.Chmod(os.Getenv("BUILDER_WORK_PATH"), 0o700) })
	run(t, conn, 40, map[string]any{"ref": "r:1"}, map[string]any{})
	end, _, _ := c.itemEnd(t, 10*time.Second)
	if !strings.Contains(end["message"].(string), "the build failed") {
		t.Errorf("end %v", end)
	}
}

// TestBuildAborted checks a build cancelled while it builds, a second run of the same job
// claimed again, another job refused as busy, and a cancel for a job it does not run.
func TestBuildAborted(t *testing.T) {
	started := make(chan struct{})
	fakes(t, func(ctx context.Context, _ io.Writer) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	c, conn, _ := start(t)
	run(t, conn, 50, map[string]any{"ref": "r:1"}, map[string]any{})
	<-started
	run(t, conn, 50, map[string]any{"ref": "r:1"}, map[string]any{})
	c.next(t, "claimed", 5*time.Second)
	if m := c.next(t, "claimed", 5*time.Second); m["jobId"].(float64) != 50 {
		t.Errorf("claimed %v", m)
	}
	run(t, conn, 51, map[string]any{"ref": "r:1"}, map[string]any{})
	if m := c.next(t, "refused", 5*time.Second); m["busy"] != true {
		t.Errorf("another job: %v", m)
	}
	send(t, conn, socket.Message{"t": "cancel", "jobId": "99"})
	send(t, conn, socket.Message{"t": "cancel", "jobId": "50"})
	end, _, _ := c.itemEnd(t, 10*time.Second)
	if end["status"] != "aborted" {
		t.Errorf("end %v", end)
	}
}

// TestStoppingRefuses checks that a builder that stops takes no build, and says so in its health.
func TestStoppingRefuses(t *testing.T) {
	fakes(t, pushed)
	isStopping = func() bool { return true }
	c, conn, b := start(t)
	run(t, conn, 60, map[string]any{"ref": "r:1"}, map[string]any{})
	if m := c.next(t, "refused", 5*time.Second); m["busy"] != true || !strings.Contains(m["error"].(string), "stopping") {
		t.Errorf("refused %v", m)
	}
	send(t, conn, socket.Message{"t": "run", "jobId": 61, "contract": socket.SocketContract, "sealed": map[string]any{}, "build": "text"})
	c.next(t, "refused", 5*time.Second)
	eventually(t, func() bool { return b.IsWelcomed() })
	b.SendHealth()
	if m := c.next(t, "health", 5*time.Second); m["stopping"] != true {
		t.Errorf("health %v", m)
	}
}

// TestUnreadableBuild checks a build that is no object.
func TestUnreadableBuild(t *testing.T) {
	fakes(t, pushed)
	c, conn, _ := start(t)
	sealed, _ := socket.SealJobSecrets(map[string]any{}, "token-0123456789abcdef", 62)
	send(t, conn, socket.Message{"t": "run", "jobId": 62, "contract": socket.SocketContract, "sealed": sealed, "build": "text"})
	if m := c.next(t, "refused", 5*time.Second); !strings.Contains(m["error"].(string), "cannot be read") {
		t.Errorf("refused %v", m)
	}
}

// eventually waits for a condition, at most 5 seconds.
//
// Args:
//
//	t: the test.
//	ok: the condition.
func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 250 && !ok(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !ok() {
		t.Fatal("not in 5 seconds")
	}
}

// ─── the builder's state, driven directly ────────────────────────────────────

// pairSession opens a socket to a test server and hands both ends over, the session running
// into the builder.
//
// Args:
//
//	t: the test.
//	b: the builder that reads the session.
//
// Returns:
//
//	The session, and the server's end.
func pairSession(t *testing.T, b *Builder) (*socket.Session, *websocket.Conn) {
	t.Helper()
	ends := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ends <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := socket.NewSession(conn, "test")
	go s.Run(func(m socket.Message) { b.Handle(s, m) })
	return s, <-ends
}

// TestState checks a newer socket replacing the older, a welcome that waits for approval, a
// message of no known type, acks of a job it does not hold and of one still sending, items not
// sent on a closed socket, an item missing from the spool, and the small readers.
func TestState(t *testing.T) {
	t.Setenv("BUILDER_WORK_PATH", t.TempDir())
	b := New()
	old, oldPeer := pairSession(t, b)
	b.Attach(old)
	s, _ := pairSession(t, b)
	b.Attach(s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := oldPeer.Read(ctx); err != nil {
			if code := websocket.CloseStatus(err); int(code) != socket.CloseReplaced {
				t.Errorf("the older socket closed with %d", code)
			}
			break
		}
	}
	b.Detach(old)
	b.Handle(s, socket.Message{"t": "welcome", "pending": true})
	b.Handle(s, socket.Message{"t": "nothing"})
	if !b.IsWelcomed() {
		t.Fatal("not welcomed")
	}

	// a job of its own, its items sent, acked part by part
	_ = b.ResumeSpools()
	b.mu.Lock()
	b.jobs[70] = &job{spool: socket.OpenSpool(spoolDir(), 70), running: true, cancel: func() {}}
	b.emit(70, socket.Item{"kind": "output"})
	b.emit(70, socket.Item{"kind": "output"})
	b.mu.Unlock()
	// messages read from JSON : their numbers are float64
	b.Handle(s, socket.Message{"t": "ack", "jobId": 71.0, "seq": 1.0})
	b.Handle(s, socket.Message{"t": "ack", "jobId": 70.0, "seq": 1.0})
	// a hello again with the job held, and a welcome that sends what is not acked
	b.Attach(s)
	b.Handle(s, socket.Message{"t": "welcome", "node": "app-2"})

	// an item the spool no longer has : skipped
	b.mu.Lock()
	b.jobs[70].spool.Seq++
	b.pump(70)
	b.emit(99, socket.Item{"kind": "output"})
	b.mu.Unlock()
	b.say(99, "x", "stdout")
	b.say(70, "", "stdout")

	// the socket closed : the send fails, and the spool goes back to resend later
	s.Terminate()
	time.Sleep(50 * time.Millisecond)
	b.mu.Lock()
	b.emit(70, socket.Item{"kind": "output"})
	next, acked := b.jobs[70].spool.Next, b.jobs[70].spool.Acked
	b.mu.Unlock()
	if next != acked+1 {
		t.Errorf("not rewound: next %d, acked %d", next, acked)
	}
	b.Detach(s)
	if b.IsWelcomed() {
		t.Error("welcomed without a socket")
	}

	// the small readers
	for v, want := range map[any]bool{nil: false, true: true, 0.0: false, 2.0: true, "": false, "x": true} {
		if truthy(v) != want {
			t.Errorf("truthy(%v)", v)
		}
	}
	if truthy([]any{}) != true || text(nil) != "" || text(3.0) != "3" || text("x") != "x" {
		t.Error("truthy or text")
	}
	if jobIDOf("12abc") != 12 || jobIDOf(true) != 0 || jobIDOf(4.0) != 4 {
		t.Error("jobIDOf")
	}
}

// TestResumeSpools checks the builds an earlier start left: one ended is kept to resend, one
// that was running is said abandoned, other files are left alone, and folders that cannot be
// made or read.
func TestResumeSpools(t *testing.T) {
	work := t.TempDir()
	t.Setenv("BUILDER_WORK_PATH", work)
	dir := filepath.Join(work, "spool")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(socket.SpoolFile(dir, 3), []byte(`{"kind":"end","seq":1}`+"\n"), 0o600)
	_ = os.WriteFile(socket.SpoolFile(dir, 4), []byte(`{"kind":"output","seq":1}`+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600)
	b := New()
	if err := b.ResumeSpools(); err != nil {
		t.Fatal(err)
	}
	if len(b.jobs) != 2 || b.jobs[4].spool.Seq != 2 || b.jobs[3].spool.Seq != 1 {
		t.Errorf("jobs %v", b.jobs)
	}
	// no socket yet : nothing is sent, and a job resumed has nothing to cancel
	b.mu.Lock()
	b.pump(3)
	b.jobs[3].cancel()
	b.mu.Unlock()

	_ = os.Chmod(dir, 0o000)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if New().ResumeSpools() == nil {
		t.Error("a spool folder that cannot be read")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	t.Setenv("BUILDER_WORK_PATH", file)
	if New().ResumeSpools() == nil {
		t.Error("a work folder under a file")
	}
}
