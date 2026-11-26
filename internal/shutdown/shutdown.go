// Package shutdown stops the builder: SIGTERM or SIGINT runs what each part registered, the last
// registered first, then the process exits. A closer that hangs does not keep it: it exits after
// ten seconds whatever is left.
package shutdown

import (
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ansibleforms/builder/internal/logger"
)

// closer is one thing to close when the process stops.
type closer struct {
	name string
	fn   func() error
}

var (
	mu       sync.Mutex
	closers  []closer
	stopping atomic.Bool
)

// IsStopping tells whether the process is stopping.
//
// Returns:
//
//	True once SIGTERM or SIGINT came.
func IsStopping() bool { return stopping.Load() }

// OnShutdown registers what to close when the process stops.
//
// Args:
//
//	name: what it is, for the log.
//	fn: closes it.
func OnShutdown(name string, fn func() error) {
	mu.Lock()
	defer mu.Unlock()
	closers = append(closers, closer{name, fn})
}

// Install starts waiting for SIGTERM and SIGINT.
func Install() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-ch
		stop(sig.String())
	}()
}

// exit ends the process, and hangLimit is how long the closers may take before it ends anyway ;
// the tests replace both.
var (
	exit      = os.Exit
	hangLimit = 10 * time.Second
)

// stop runs every closer, the last registered first, then exits.
//
// Args:
//
//	signal: the signal received, for the log.
func stop(signal string) {
	if !stopping.CompareAndSwap(false, true) {
		return
	}
	name := map[string]string{"terminated": "SIGTERM", "interrupt": "SIGINT"}[signal]
	if name == "" {
		name = signal
	}
	logger.Notice("%s: stopping", name)
	// a closer that hangs does not keep the process
	timer := time.AfterFunc(hangLimit, func() { exit(0) })
	defer timer.Stop()
	closeAll()
	exit(0)
}

// closeAll runs every closer, the last registered first ; one that fails is logged and the
// others still run.
func closeAll() {
	mu.Lock()
	list := append([]closer(nil), closers...)
	mu.Unlock()
	for i := len(list) - 1; i >= 0; i-- {
		if err := list[i].fn(); err != nil {
			logger.Warning("Stopping %s failed: %v", list[i].name, err)
		}
	}
}
