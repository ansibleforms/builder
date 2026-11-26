// Package logger writes the builder's log: one line per message, its time and level first.
// LOG_LEVEL (debug, info, notice, warning, error ; notice by default) says from which level on.
// Errors and worse go to stderr, the rest to stdout.
package logger

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ─── levels ───────────────────────────────────────────────────────────────────

// levels are the log's levels, from the least to the most severe.
var levels = []string{"debug", "info", "notice", "warning", "error", "crit"}

// mu keeps two lines from being written into each other.
var mu sync.Mutex

// rank tells a level's place among the levels.
//
// Args:
//
//	level: the level's name.
//
// Returns:
//
//	Its index, -1 when it is none.
func rank(level string) int {
	for i, l := range levels {
		if l == level {
			return i
		}
	}
	return -1
}

// threshold reads LOG_LEVEL each time, so a test or a restart sees a change.
//
// Returns:
//
//	The index of the least level written: notice when LOG_LEVEL is unset, debug when it names
//	no level.
func threshold() int {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if v == "" {
		v = "notice"
	}
	return max(0, rank(v))
}

// ─── writing ──────────────────────────────────────────────────────────────────

// write writes a line at a level, when the level is at or above LOG_LEVEL.
//
// Args:
//
//	level: the level.
//	format: the message, as fmt.Sprintf takes it.
//	args: its values.
func write(level, format string, args ...any) {
	r := rank(level)
	if r < threshold() {
		return
	}
	line := fmt.Sprintf("%s %s: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), level, fmt.Sprintf(format, args...))
	mu.Lock()
	defer mu.Unlock()
	if r >= rank("error") {
		fmt.Fprint(os.Stderr, line)
		return
	}
	fmt.Fprint(os.Stdout, line)
}

// Debug writes a debug line.
func Debug(format string, args ...any) { write("debug", format, args...) }

// Info writes an info line.
func Info(format string, args ...any) { write("info", format, args...) }

// Notice writes a notice line.
func Notice(format string, args ...any) { write("notice", format, args...) }

// Warning writes a warning line.
func Warning(format string, args ...any) { write("warning", format, args...) }

// Error writes an error line.
func Error(format string, args ...any) { write("error", format, args...) }
