// Package settings reads the builder's settings from its environment. Each one is read when it
// is asked for, so a test can set the environment first.
package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Port is the port of its /live and /ready (PORT, 8000 by default).
//
// Returns:
//
//	The port.
func Port() int {
	if p, err := strconv.Atoi(strings.TrimSpace(os.Getenv("PORT"))); err == nil && p > 0 {
		return p
	}
	return 8000
}

// NodeID is the name it goes by among the controller's nodes: builder-<hostname>-<port>, the
// same after a restart of the same container.
//
// Returns:
//
//	The node id.
func NodeID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("builder-%s-%d", host, Port())
}

// BuilderName is its name in the Designer's RTE builders (RTE_NAME), its node id by default.
//
// Returns:
//
//	The name.
func BuilderName() string {
	if n := strings.TrimSpace(os.Getenv("RTE_NAME")); n != "" {
		return n
	}
	return NodeID()
}

// ControllerURL is the controller's address it dials (CONTROLLER_URL, APP_URL before 7.6).
//
// Returns:
//
//	The address, empty when neither is set.
func ControllerURL() string {
	if u := os.Getenv("CONTROLLER_URL"); u != "" {
		return u
	}
	return os.Getenv("APP_URL")
}

// ControllerTLSVerify tells whether it checks the controller's certificate
// (CONTROLLER_TLS_VERIFY=0 : a self-signed one, in one trusted network only ; APP_TLS_VERIFY
// before 7.6).
//
// Returns:
//
//	False only when the setting is 0.
func ControllerTLSVerify() bool {
	if v, ok := os.LookupEnv("CONTROLLER_TLS_VERIFY"); ok {
		return v != "0"
	}
	if v, ok := os.LookupEnv("APP_TLS_VERIFY"); ok {
		return v != "0"
	}
	return true
}

// WorkDir is where its spool, its builds' files and its own buildkitd's cache are
// (BUILDER_WORK_PATH).
//
// Returns:
//
//	The folder.
func WorkDir() string {
	if d := os.Getenv("BUILDER_WORK_PATH"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "af-builder")
}

// BuildkitHost is the BuildKit it builds with: BUILDKIT_HOST, else the rootless buildkitd it
// starts itself.
//
// Returns:
//
//	The BuildKit's address (unix:///..., tcp://...).
func BuildkitHost() string {
	if h := os.Getenv("BUILDKIT_HOST"); h != "" {
		return h
	}
	return "unix://" + filepath.Join(WorkDir(), "buildkit", "buildkitd.sock")
}

// Token is its own token (RTE_TOKEN): the controller keeps it for it once it joins.
//
// Returns:
//
//	The token.
func Token() string { return os.Getenv("RTE_TOKEN") }

// JoinToken is the join token (RTE_JOIN_TOKEN) that approves it at once, the first time.
//
// Returns:
//
//	The join token, empty when it has none.
func JoinToken() string { return os.Getenv("RTE_JOIN_TOKEN") }

// ListenHost is the address its probes listen on (LISTEN_HOST), every interface by default.
//
// Returns:
//
//	The host.
func ListenHost() string {
	if h := strings.TrimSpace(os.Getenv("LISTEN_HOST")); h != "" {
		return h
	}
	return "0.0.0.0"
}

// Problems says why the builder cannot start, when it cannot: its token and the controller's
// address.
//
// Returns:
//
//	The problems ; none when it can start.
func Problems() []string {
	var problems []string
	if len(Token()) < 16 {
		problems = append(problems, "set RTE_TOKEN (at least 16 characters)")
	}
	if ControllerURL() == "" {
		problems = append(problems, "set CONTROLLER_URL, the controller's address it dials")
	}
	return problems
}
