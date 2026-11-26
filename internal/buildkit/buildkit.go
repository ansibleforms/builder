// Package buildkit is the BuildKit a builder builds with: BUILDKIT_HOST when it names one, else a
// rootless buildkitd it starts in its own container (the image has buildkitd, rootlesskit and the
// subordinate ids). It builds through BuildKit's own client, the one buildctl is made of.
package buildkit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/cli/cli/config"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"

	"github.com/ansibleforms/builder/internal/logger"
	"github.com/ansibleforms/builder/internal/settings"
)

// ─── its own buildkitd ────────────────────────────────────────────────────────

// rootlesskit is the program that starts buildkitd in a user namespace ; the tests name a fake.
var rootlesskit = "rootlesskit"

// socketTries and socketEvery are how long it waits for buildkitd's socket: 60 times 500 ms.
var (
	socketTries = 60
	socketEvery = 500 * time.Millisecond
)

// Start starts its own rootless buildkitd, when no BUILDKIT_HOST is given, and waits for its
// socket.
//
// Returns:
//
//	What stops it (it does nothing for a BuildKit of BUILDKIT_HOST), and true once its socket
//	is there ; false when it could not start or after 30 seconds without its socket.
func Start() (func() error, bool) {
	none := func() error { return nil }
	if os.Getenv("BUILDKIT_HOST") != "" {
		return none, true
	}
	dir := filepath.Join(settings.WorkDir(), "buildkit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("buildkitd's folder cannot be made: %v", err)
		return none, false
	}
	sock := filepath.Join(dir, "buildkitd.sock")
	cmd := exec.Command(rootlesskit, "buildkitd", "--oci-worker-no-process-sandbox", "--addr", "unix://"+sock, "--root", filepath.Join(dir, "root"))
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		logger.Error("buildkitd could not start (%v): set BUILDKIT_HOST to a BuildKit the builder can reach", err)
		return none, false
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			logger.Debug("buildkitd: %s", strings.TrimSpace(sc.Text()))
		}
	}()
	// stopped : the builder stops it, so its exit is no error
	var stopped atomic.Bool
	go func() {
		err := cmd.Wait()
		if !stopped.Load() {
			logger.Error("buildkitd stopped (%v): the container needs --security-opt seccomp=unconfined --security-opt apparmor=unconfined", err)
		}
	}()
	stop := func() error {
		stopped.Store(true)
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	for i := 0; i < socketTries; i++ {
		if _, err := os.Stat(sock); err == nil {
			return stop, true
		}
		time.Sleep(socketEvery)
	}
	logger.Error("buildkitd did not open its socket within %s", time.Duration(socketTries)*socketEvery)
	return stop, false
}

// ─── its platforms ────────────────────────────────────────────────────────────

// archPlatforms are the platforms an image's arch asks for: one image, or one index with both.
var archPlatforms = map[string][]string{
	"amd64": {"linux/amd64"},
	"arm64": {"linux/arm64"},
	"all":   {"linux/amd64", "linux/arm64"},
}

// QEMUHint is how to let a host build another architecture: QEMU registered on it, once.
const QEMUHint = "docker run --privileged --rm tonistiigi/binfmt --install all"

// PlatformsOf says the platforms a build's arch asks for.
//
// Args:
//
//	arch: amd64, arm64 or all ; empty (a controller of before) : the builder's own.
//
// Returns:
//
//	The platforms (linux/amd64...) ; none for the builder's own.
//
// Errors:
//
//	When the arch is none of them.
func PlatformsOf(arch string) ([]string, error) {
	if arch == "" {
		return nil, nil
	}
	p, ok := archPlatforms[arch]
	if !ok {
		return nil, fmt.Errorf("'%s' is no architecture: amd64, arm64 or all", arch)
	}
	return p, nil
}

// Platform is one platform a worker builds.
type Platform struct {
	OS           string
	Architecture string
}

// PlatformsOfWorkers lists the platforms BuildKit's workers can build: another architecture is
// among them only when QEMU is registered on the host.
//
// Args:
//
//	workers: each worker's platforms.
//
// Returns:
//
//	os/architecture, each once, in the order met.
func PlatformsOfWorkers(workers [][]Platform) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range workers {
		for _, p := range w {
			if p.OS == "" || p.Architecture == "" {
				continue
			}
			k := p.OS + "/" + p.Architecture
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// Client is what the builder asks of BuildKit: its workers, a build, and closing.
type Client interface {
	ListWorkers(ctx context.Context, opts ...client.ListWorkersOption) ([]*client.WorkerInfo, error)
	Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, status chan *client.SolveStatus) (*client.SolveResponse, error)
	Close() error
}

// connect opens a client to the BuildKit ; the tests give a fake one.
//
// Args:
//
//	ctx: the context.
//
// Returns:
//
//	The client ; its Close closes it.
//
// Errors:
//
//	When the address is no BuildKit's.
var connect = func(ctx context.Context) (Client, error) {
	return client.New(ctx, settings.BuildkitHost())
}

// BuildablePlatforms lists the platforms BuildKit can build now.
//
// Returns:
//
//	os/architecture ; nil when BuildKit could not say.
func BuildablePlatforms() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := connect(ctx)
	if err != nil {
		return nil
	}
	defer c.Close()
	workers, err := c.ListWorkers(ctx)
	if err != nil {
		return nil
	}
	var all [][]Platform
	for _, w := range workers {
		var ps []Platform
		for _, p := range w.Platforms {
			ps = append(ps, Platform{OS: p.OS, Architecture: p.Architecture})
		}
		all = append(all, ps)
	}
	return PlatformsOfWorkers(all)
}

// ─── building ─────────────────────────────────────────────────────────────────

// Build is one image to build and push.
type Build struct {
	// Context is the folder of its files, the Dockerfile among them.
	Context string
	// DockerConfig is the folder of the registry's login (registry.WriteDockerConfig).
	DockerConfig string
	// Ref is the reference it is pushed to.
	Ref string
	// Platforms are the platforms it is built for ; none : the builder's own.
	Platforms []string
	// SBOM pushes an SBOM with the image.
	SBOM bool
	// Insecure pushes to a registry over plain http.
	Insecure bool
}

// Run builds the image with the Dockerfile frontend, pushes it, and writes BuildKit's progress
// as buildctl --progress=plain does.
//
// Args:
//
//	ctx: the context ; cancelled, the build stops.
//	b: the build.
//	out: where the progress goes.
//
// Returns:
//
//	The pushed image's digest, "" when BuildKit did not say it.
//
// Errors:
//
//	When BuildKit cannot be reached, the build fails or is cancelled.
func Run(ctx context.Context, b Build, out io.Writer) (string, error) {
	c, err := connect(ctx)
	if err != nil {
		return "", fmt.Errorf("BuildKit at %s cannot be reached: %w", settings.BuildkitHost(), err)
	}
	defer c.Close()
	files, err := fsutil.NewFS(b.Context)
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(b.DockerConfig)
	if err != nil {
		return "", fmt.Errorf("the registry login cannot be read: %w", err)
	}
	attrs := map[string]string{"name": b.Ref, "push": "true"}
	// a registry over plain http (a registry address with http://) : pushed to without TLS
	if b.Insecure {
		attrs["registry.insecure"] = "true"
	}
	frontend := map[string]string{}
	if len(b.Platforms) > 0 {
		frontend["platform"] = strings.Join(b.Platforms, ",")
	}
	if b.SBOM {
		frontend["attest:sbom"] = ""
	}
	opt := client.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: frontend,
		LocalMounts:   map[string]fsutil.FS{"context": files, "dockerfile": files},
		Exports:       []client.ExportEntry{{Type: client.ExporterImage, Attrs: attrs}},
		Session: []session.Attachable{authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
			AuthConfigProvider: authprovider.LoadAuthConfig(cfg),
		})},
	}
	// the plain display takes any writer : it has no error to give
	display, _ := progressui.NewDisplay(out, progressui.PlainMode)
	status := make(chan *client.SolveStatus)
	var res *client.SolveResponse
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		var err error
		res, err = c.Solve(ctx, nil, opt, status)
		return err
	})
	eg.Go(func() error {
		_, err := display.UpdateFrom(context.WithoutCancel(ctx), status)
		return err
	})
	if err := eg.Wait(); err != nil {
		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}
		return "", err
	}
	return res.ExporterResponse["containerimage.digest"], nil
}
