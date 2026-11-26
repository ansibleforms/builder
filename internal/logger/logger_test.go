package logger

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

// capture runs a function with stdout and stderr read into strings.
//
// Args:
//
//	t: the test.
//	fn: what writes.
//
// Returns:
//
//	What went to stdout, and what went to stderr.
func capture(t *testing.T, fn func()) (string, string) {
	t.Helper()
	read := func(f **os.File) (func() string, func()) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		was := *f
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = was; return <-done }, func() {}
	}
	out, _ := read(&os.Stdout)
	errs, _ := read(&os.Stderr)
	fn()
	return out(), errs()
}

// TestThreshold checks which levels LOG_LEVEL lets through: notice by default, debug for a
// level it does not know.
func TestThreshold(t *testing.T) {
	for level, want := range map[string]string{"": "notice", "WARNING": "warning", "debug": "debug", "loud": "debug"} {
		t.Setenv("LOG_LEVEL", level)
		if got := levels[threshold()]; got != want {
			t.Errorf("LOG_LEVEL=%q lets %s through, want %s", level, got, want)
		}
	}
}

// TestLines checks a line's form, the levels below LOG_LEVEL left out, and errors on stderr.
func TestLines(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")
	out, errs := capture(t, func() {
		Debug("hidden")
		Info("one %d", 1)
		Notice("between")
		Warning("two")
		Error("three")
	})
	if strings.Contains(out+errs, "hidden") {
		t.Error("a debug line went through at info")
	}
	line := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z info: one 1$`)
	if first := strings.Split(out, "\n")[0]; !line.MatchString(first) {
		t.Errorf("line %q", first)
	}
	if !strings.Contains(out, "notice: between") || !strings.Contains(out, "warning: two") || strings.Contains(out, "three") || !strings.Contains(errs, "error: three") {
		t.Errorf("stdout %q, stderr %q", out, errs)
	}
}
