package buildkit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// TestPlatformsOf checks that it reads amd64, arm64 and all, and none as the builder's own.
func TestPlatformsOf(t *testing.T) {
	for arch, want := range map[string][]string{
		"amd64": {"linux/amd64"},
		"arm64": {"linux/arm64"},
		"all":   {"linux/amd64", "linux/arm64"},
		"":      nil,
	} {
		got, err := PlatformsOf(arch)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("PlatformsOf(%q) = %v, %v ; want %v", arch, got, err, want)
		}
	}
	if _, err := PlatformsOf("s390x"); err == nil {
		t.Error("s390x was taken")
	}
}

// TestPlatformsOfWorkers checks that it lists what the workers build, each once.
func TestPlatformsOfWorkers(t *testing.T) {
	workers := [][]Platform{{{"linux", "amd64"}, {"linux", "amd64"}, {"linux", "386"}, {"", "arm"}}}
	if got := PlatformsOfWorkers(workers); !reflect.DeepEqual(got, []string{"linux/amd64", "linux/386"}) {
		t.Errorf("got %v", got)
	}
	if got := PlatformsOfWorkers(nil); len(got) != 0 {
		t.Errorf("no workers: %v", got)
	}
}

// ─── its own buildkitd ────────────────────────────────────────────────────────

// fakeRootlesskit writes a script that acts as rootlesskit buildkitd: it says a line on stderr,
// makes the socket unless FAKE_MODE=nosock, and exits at once with FAKE_MODE=exit.
//
// Args:
//
//	t: the test.
func fakeRootlesskit(t *testing.T) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "rootlesskit")
	body := "#!/bin/sh\necho starting >&2\nsock=\"${4#unix://}\"\n[ \"$FAKE_MODE\" = nosock ] || touch \"$sock\"\n[ \"$FAKE_MODE\" = exit ] && exit 3\nexec sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	was, tries, every := rootlesskit, socketTries, socketEvery
	rootlesskit, socketTries, socketEvery = script, 5, 20*time.Millisecond
	t.Cleanup(func() { rootlesskit, socketTries, socketEvery = was, tries, every })
	t.Setenv("BUILDKIT_HOST", "")
	os.Unsetenv("BUILDKIT_HOST")
	t.Setenv("BUILDER_WORK_PATH", t.TempDir())
	t.Setenv("LOG_LEVEL", "debug")
}

// TestStartWithBuildkitHost checks that another BuildKit starts nothing.
func TestStartWithBuildkitHost(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "tcp://buildkitd:1234")
	stop, ok := Start()
	if !ok || stop() != nil {
		t.Error("BUILDKIT_HOST: nothing to start, nothing to stop")
	}
}

// TestStart checks that its buildkitd starts, is waited for until its socket is there, and stops.
func TestStart(t *testing.T) {
	fakeRootlesskit(t)
	stop, ok := Start()
	if !ok {
		t.Fatal("its buildkitd did not start")
	}
	time.Sleep(100 * time.Millisecond)
	if err := stop(); err != nil {
		t.Error(err)
	}
}

// TestStartFailures checks a buildkitd that opens no socket, one that exits, one that cannot
// start, and a work folder that cannot be made.
func TestStartFailures(t *testing.T) {
	fakeRootlesskit(t)
	t.Setenv("FAKE_MODE", "nosock")
	if stop, ok := Start(); ok {
		t.Error("no socket, yet started")
	} else {
		_ = stop()
	}
	t.Setenv("FAKE_MODE", "exit")
	t.Setenv("BUILDER_WORK_PATH", t.TempDir())
	if _, ok := Start(); !ok {
		t.Error("the socket was made before it exited")
	}
	time.Sleep(100 * time.Millisecond)
	rootlesskit = filepath.Join(t.TempDir(), "none")
	if _, ok := Start(); ok {
		t.Error("a missing rootlesskit started")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	t.Setenv("BUILDER_WORK_PATH", file)
	if _, ok := Start(); ok {
		t.Error("a work folder under a file started")
	}
}

// ─── a fake BuildKit ──────────────────────────────────────────────────────────

// fake is a BuildKit client that answers as it is told.
type fake struct {
	workers []*client.WorkerInfo
	err     error
	digest  string
	got     client.SolveOpt
}

// ListWorkers gives its workers, or its error.
func (f *fake) ListWorkers(context.Context, ...client.ListWorkersOption) ([]*client.WorkerInfo, error) {
	return f.workers, f.err
}

// Solve says one step on the status channel, closes it as BuildKit does, and gives its digest
// or its error ; a cancelled context, its error.
func (f *fake) Solve(ctx context.Context, _ *llb.Definition, opt client.SolveOpt, status chan *client.SolveStatus) (*client.SolveResponse, error) {
	f.got = opt
	defer close(status)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	now := time.Now()
	status <- &client.SolveStatus{Vertexes: []*client.Vertex{{Digest: digest.FromString("v"), Name: "[1/1] FROM busybox", Started: &now, Completed: &now}}}
	if f.err != nil {
		return nil, f.err
	}
	return &client.SolveResponse{ExporterResponse: map[string]string{"containerimage.digest": f.digest}}, nil
}

// Close closes nothing.
func (f *fake) Close() error { return nil }

// useFake makes connect give a fake, or an error.
//
// Args:
//
//	t: the test.
//	f: the fake ; nil : connect fails.
func useFake(t *testing.T, f *fake) {
	was := connect
	connect = func(context.Context) (Client, error) {
		if f == nil {
			return nil, errors.New("no BuildKit")
		}
		return f, nil
	}
	t.Cleanup(func() { connect = was })
}

// TestBuildablePlatforms checks the platforms read from the workers, and nil when BuildKit
// cannot say.
func TestBuildablePlatforms(t *testing.T) {
	useFake(t, &fake{workers: []*client.WorkerInfo{{Platforms: []ocispecs.Platform{{OS: "linux", Architecture: "arm64"}}}}})
	if got := BuildablePlatforms(); !reflect.DeepEqual(got, []string{"linux/arm64"}) {
		t.Errorf("got %v", got)
	}
	useFake(t, &fake{err: errors.New("down")})
	if BuildablePlatforms() != nil {
		t.Error("a failing ListWorkers")
	}
	useFake(t, nil)
	if BuildablePlatforms() != nil {
		t.Error("no BuildKit")
	}
}

// build makes a build's folders: its context with a Dockerfile, and its registry login.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The build.
func build(t *testing.T) Build {
	dir := t.TempDir()
	ctx := filepath.Join(dir, "context")
	_ = os.Mkdir(ctx, 0o700)
	_ = os.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte("FROM busybox\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{}}`), 0o600)
	return Build{Context: ctx, DockerConfig: dir, Ref: "r.example.com/a:1"}
}

// TestRun checks a build's options, its progress written, and its digest.
func TestRun(t *testing.T) {
	f := &fake{digest: "sha256:abc"}
	useFake(t, f)
	b := build(t)
	b.Platforms, b.SBOM, b.Insecure = []string{"linux/amd64", "linux/arm64"}, true, true
	var out strings.Builder
	got, err := Run(context.Background(), b, &out)
	if err != nil || got != "sha256:abc" {
		t.Fatalf("Run = %q, %v", got, err)
	}
	attrs := f.got.Exports[0].Attrs
	if attrs["name"] != b.Ref || attrs["push"] != "true" || attrs["registry.insecure"] != "true" {
		t.Errorf("export %v", attrs)
	}
	if f.got.FrontendAttrs["platform"] != "linux/amd64,linux/arm64" || f.got.Frontend != "dockerfile.v0" {
		t.Errorf("frontend %v", f.got.FrontendAttrs)
	}
	if _, ok := f.got.FrontendAttrs["attest:sbom"]; !ok {
		t.Error("no SBOM asked")
	}
	if !strings.Contains(out.String(), "FROM busybox") {
		t.Errorf("progress %q", out.String())
	}
}

// TestRunFailures checks no BuildKit, a missing context, an unreadable login, a failed build
// and a cancelled one.
func TestRunFailures(t *testing.T) {
	useFake(t, nil)
	if _, err := Run(context.Background(), build(t), io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be reached") {
		t.Errorf("no BuildKit: %v", err)
	}
	useFake(t, &fake{})
	b := build(t)
	b.Context = filepath.Join(t.TempDir(), "none")
	if _, err := Run(context.Background(), b, io.Discard); err == nil {
		t.Error("a missing context")
	}
	b = build(t)
	_ = os.WriteFile(filepath.Join(b.DockerConfig, "config.json"), []byte("{"), 0o600)
	if _, err := Run(context.Background(), b, io.Discard); err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("an unreadable login: %v", err)
	}
	useFake(t, &fake{err: errors.New("RUN failed")})
	if _, err := Run(context.Background(), build(t), io.Discard); err == nil || !strings.Contains(err.Error(), "RUN failed") {
		t.Errorf("a failed build: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	useFake(t, &fake{})
	if _, err := Run(ctx, build(t), io.Discard); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled build: %v", err)
	}
}

// TestConnect checks that the real client is made for BUILDKIT_HOST (it dials lazily).
func TestConnect(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "tcp://127.0.0.1:1")
	c, err := connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}
