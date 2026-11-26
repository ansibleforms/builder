package settings

import (
	"os"
	"strings"
	"testing"
)

// TestDefaults checks the settings of a builder given only what it must have.
func TestDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "RTE_NAME", "CONTROLLER_URL", "APP_URL", "CONTROLLER_TLS_VERIFY", "APP_TLS_VERIFY", "BUILDER_WORK_PATH", "BUILDKIT_HOST", "LISTEN_HOST"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	host, _ := os.Hostname()
	if Port() != 8000 || NodeID() != "builder-"+host+"-8000" || BuilderName() != NodeID() {
		t.Errorf("port %d, node %s, name %s", Port(), NodeID(), BuilderName())
	}
	if !ControllerTLSVerify() || ListenHost() != "0.0.0.0" {
		t.Error("it checks the certificate and listens everywhere by default")
	}
	if !strings.HasPrefix(BuildkitHost(), "unix://"+WorkDir()) || !strings.HasSuffix(BuildkitHost(), "/buildkit/buildkitd.sock") {
		t.Errorf("BuildKit at %s", BuildkitHost())
	}
}

// TestNamesAndAddresses checks what the environment changes: its name, its port, the
// controller's address (APP_URL before 7.6) and its BuildKit.
func TestNamesAndAddresses(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("RTE_NAME", "  builder-1 ")
	t.Setenv("CONTROLLER_URL", "")
	t.Setenv("APP_URL", "https://old.example.com")
	t.Setenv("BUILDKIT_HOST", "tcp://buildkitd:1234")
	if Port() != 9000 || !strings.HasSuffix(NodeID(), "-9000") || BuilderName() != "builder-1" {
		t.Errorf("port %d, node %s, name %q", Port(), NodeID(), BuilderName())
	}
	if ControllerURL() != "https://old.example.com" || BuildkitHost() != "tcp://buildkitd:1234" {
		t.Errorf("controller %s, BuildKit %s", ControllerURL(), BuildkitHost())
	}
	t.Setenv("CONTROLLER_URL", "https://forms.example.com")
	if ControllerURL() != "https://forms.example.com" {
		t.Errorf("CONTROLLER_URL comes before APP_URL: %s", ControllerURL())
	}
}

// TestTLSVerify checks that only 0 turns the check off, CONTROLLER_TLS_VERIFY before
// APP_TLS_VERIFY.
func TestTLSVerify(t *testing.T) {
	t.Setenv("APP_TLS_VERIFY", "0")
	os.Unsetenv("CONTROLLER_TLS_VERIFY")
	if ControllerTLSVerify() {
		t.Error("APP_TLS_VERIFY=0 alone")
	}
	t.Setenv("CONTROLLER_TLS_VERIFY", "1")
	if !ControllerTLSVerify() {
		t.Error("CONTROLLER_TLS_VERIFY=1 comes first")
	}
	t.Setenv("CONTROLLER_TLS_VERIFY", "false")
	if !ControllerTLSVerify() {
		t.Error("only 0 turns it off")
	}
}

// TestProblems checks that a short token and a missing address keep it from starting.
func TestProblems(t *testing.T) {
	t.Setenv("RTE_TOKEN", "short")
	t.Setenv("CONTROLLER_URL", "")
	t.Setenv("APP_URL", "")
	if p := Problems(); len(p) != 2 {
		t.Errorf("problems %v", p)
	}
	t.Setenv("RTE_TOKEN", "token-0123456789abcdef")
	t.Setenv("CONTROLLER_URL", "https://forms.example.com")
	if p := Problems(); len(p) != 0 {
		t.Errorf("problems %v", p)
	}
}

// TestListenHost checks LISTEN_HOST, every interface without it.
func TestListenHost(t *testing.T) {
	t.Setenv("LISTEN_HOST", " 127.0.0.1 ")
	if ListenHost() != "127.0.0.1" {
		t.Errorf("listens on %q", ListenHost())
	}
}

// TestTokens checks its token, its join token and its work folder, read as they are.
func TestTokens(t *testing.T) {
	t.Setenv("BUILDER_WORK_PATH", "/work")
	if WorkDir() != "/work" {
		t.Errorf("work folder %s", WorkDir())
	}
	t.Setenv("RTE_TOKEN", "token-0123456789abcdef")
	t.Setenv("RTE_JOIN_TOKEN", "afj_x")
	if Token() != "token-0123456789abcdef" || JoinToken() != "afj_x" {
		t.Errorf("token %q, join token %q", Token(), JoinToken())
	}
}
