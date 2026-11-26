package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ansibleforms/builder/internal/version"
)

// output runs a command with stdout and stderr read.
//
// Args:
//
//	t: the test.
//	args: its arguments.
//
// Returns:
//
//	Its exit code, and what it wrote to stdout and to stderr.
func output(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	pipe := func(f **os.File) func() string {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		was := *f
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = was; return <-done }
	}
	out, errs := pipe(&os.Stdout), pipe(&os.Stderr)
	code := command(args)
	return code, out(), errs()
}

// TestVersion checks that version prints the builder's version.
func TestVersion(t *testing.T) {
	code, out, _ := output(t, "version")
	if code != 0 || strings.TrimSpace(out) != version.App {
		t.Errorf("exit %d, printed %q", code, out)
	}
}

// TestUsage checks --help and a command it does not know.
func TestUsage(t *testing.T) {
	if code, _, errs := output(t, "--help"); code != 0 || !strings.Contains(errs, "health") {
		t.Errorf("--help: exit %d, %q", code, errs)
	}
	if code, _, errs := output(t, "build"); code != 2 || !strings.Contains(errs, "'build' is no command") {
		t.Errorf("an unknown command: exit %d, %q", code, errs)
	}
	if code, _, _ := output(t, "--nope"); code != 2 {
		t.Errorf("an unknown flag: exit %d", code)
	}
}

// TestHealth checks that health is 0 while its port takes a connection, 1 once nothing listens.
func TestHealth(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
	if code, _, _ := output(t, "health"); code != 0 {
		t.Errorf("listening: exit %d", code)
	}
	l.Close()
	if code, _, _ := output(t, "health"); code != 1 {
		t.Errorf("nothing listening: exit %d", code)
	}
}

// ─── run ──────────────────────────────────────────────────────────────────────

// runEnv sets what run needs: its token, a controller that is not there, a work folder, a free
// port, its BuildKit started at once, its health every 10 ms, and a fresh stopped.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	Its port.
func runEnv(t *testing.T) int {
	t.Helper()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv("PORT", strconv.Itoa(port))
	t.Setenv("LISTEN_HOST", "127.0.0.1")
	t.Setenv("RTE_TOKEN", "token-0123456789abcdef")
	t.Setenv("CONTROLLER_URL", "http://127.0.0.1:1")
	t.Setenv("BUILDER_WORK_PATH", t.TempDir())
	wasStart, wasEvery, wasStopped := startBuildkit, healthEvery, stopped
	startBuildkit = func() (func() error, bool) { return func() error { return nil }, true }
	healthEvery, stopped = 10*time.Millisecond, make(chan struct{})
	t.Cleanup(func() { startBuildkit, healthEvery, stopped = wasStart, wasEvery, wasStopped })
	return port
}

// get asks a probe.
//
// Args:
//
//	port: the builder's port.
//	path: the probe.
//
// Returns:
//
//	Its status, 0 when it does not answer.
func get(port int, path string) int {
	res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return 0
	}
	res.Body.Close()
	return res.StatusCode
}

// TestRun starts the builder: /live answers, /ready waits for a welcome, another path is not
// one, and it returns once stopped.
func TestRun(t *testing.T) {
	port := runEnv(t)
	codes := make(chan int, 1)
	go func() { codes <- command([]string{"run"}) }()
	for i := 0; i < 100 && get(port, "/live") == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if get(port, "/live") != 200 || get(port, "/ready") != 503 || get(port, "/other") != 503 {
		t.Errorf("probes: live %d, ready %d, other %d", get(port, "/live"), get(port, "/ready"), get(port, "/other"))
	}
	time.Sleep(30 * time.Millisecond)
	close(stopped)
	if code := <-codes; code != 0 {
		t.Errorf("exit %d", code)
	}
}

// TestRunRefuses checks that it does not start without its settings, with a work folder it
// cannot make, or with a controller address that is none, and that it goes on with spools it
// cannot read and a port taken.
func TestRunRefuses(t *testing.T) {
	runEnv(t)
	t.Setenv("RTE_TOKEN", "short")
	if code, _, errs := output(t, "run"); code != 1 || !strings.Contains(errs, "RTE_TOKEN") {
		t.Errorf("no token: %d %q", code, errs)
	}
	t.Setenv("RTE_TOKEN", "token-0123456789abcdef")
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	t.Setenv("BUILDER_WORK_PATH", file)
	if code, _, errs := output(t, "run"); code != 1 || !strings.Contains(errs, "work folder") {
		t.Errorf("a work folder under a file: %d %q", code, errs)
	}
	t.Setenv("BUILDER_WORK_PATH", t.TempDir())
	t.Setenv("CONTROLLER_URL", "ftp://forms.example.com")
	if code, _, errs := output(t, "run"); code != 1 || !strings.Contains(errs, "CONTROLLER_URL") {
		t.Errorf("an ftp controller: %d %q", code, errs)
	}

	// spools it cannot read, its port taken : it says so and starts anyway
	port := runEnv(t)
	work := os.Getenv("BUILDER_WORK_PATH")
	_ = os.MkdirAll(filepath.Join(work, "spool"), 0o700)
	_ = os.Chmod(filepath.Join(work, "spool"), 0o000)
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(work, "spool"), 0o700) })
	l, _ := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	defer l.Close()
	codes := make(chan int, 1)
	go func() { codes <- command([]string{"run"}) }()
	time.Sleep(100 * time.Millisecond)
	close(stopped)
	if code := <-codes; code != 0 {
		t.Errorf("exit %d", code)
	}
}

// TestMainExits checks that main exits with its command's code.
func TestMainExits(t *testing.T) {
	var got []int
	was, wasArgs := exit, os.Args
	exit = func(code int) { got = append(got, code) }
	t.Cleanup(func() { exit, os.Args = was, wasArgs })
	os.Args = []string{"ansibleforms-rte-builder", "nope"}
	main()
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("exited with %v", got)
	}
}
