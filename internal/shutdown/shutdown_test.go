package shutdown

import (
	"errors"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fresh starts a test with no closer registered and the process not stopping, its exits
// recorded instead of made.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The exit codes, as they come.
func fresh(t *testing.T) chan int {
	t.Helper()
	codes := make(chan int, 4)
	wasExit, wasLimit := exit, hangLimit
	exit = func(code int) { codes <- code }
	mu.Lock()
	closers = nil
	mu.Unlock()
	stopping.Store(false)
	t.Cleanup(func() { exit, hangLimit = wasExit, wasLimit })
	return codes
}

// TestStop checks that a stop runs every closer, the last registered first, goes on past one
// that fails, exits with 0, and happens once.
func TestStop(t *testing.T) {
	codes := fresh(t)
	var ran []string
	OnShutdown("first", func() error { ran = append(ran, "first"); return nil })
	OnShutdown("failing", func() error { ran = append(ran, "failing"); return errors.New("no") })
	OnShutdown("last", func() error { ran = append(ran, "last"); return nil })
	if IsStopping() {
		t.Fatal("stopping before a signal")
	}
	stop("terminated")
	if !IsStopping() {
		t.Error("not stopping after a signal")
	}
	if want := []string{"last", "failing", "first"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("ran %v, want %v", ran, want)
	}
	if code := <-codes; code != 0 {
		t.Errorf("exited with %d", code)
	}
	stop("interrupt")
	if len(ran) != 3 || len(codes) != 0 {
		t.Error("a second signal stopped again")
	}
}

// TestHangingCloser checks that a closer that hangs does not keep the process: it exits once
// hangLimit is past.
func TestHangingCloser(t *testing.T) {
	codes := fresh(t)
	hangLimit = 20 * time.Millisecond
	OnShutdown("hanging", func() error { time.Sleep(200 * time.Millisecond); return nil })
	done := make(chan struct{})
	go func() { stop("hangup"); close(done) }()
	select {
	case code := <-codes:
		if code != 0 {
			t.Errorf("exited with %d", code)
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("the hanging closer kept the process")
	}
	// the closer ends, and the stop exits too : the test ends after it
	<-done
	<-codes
}

// TestInstall checks that SIGTERM stops the process.
func TestInstall(t *testing.T) {
	codes := fresh(t)
	var once sync.Once
	closed := make(chan struct{})
	OnShutdown("probe", func() error { once.Do(func() { close(closed) }); return nil })
	Install()
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not stop it")
	}
	if code := <-codes; code != 0 {
		t.Errorf("exited with %d", code)
	}
}
