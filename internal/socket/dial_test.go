package socket

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestBuilderHeaders checks that it says it is a builder, with its token, and its join token
// when it has one.
func TestBuilderHeaders(t *testing.T) {
	t.Setenv("RTE_JOIN_TOKEN", "afj_x")
	t.Setenv("RTE_NAME", "builder-1")
	h := BuilderHeaders("token-0123456789abcdef")
	for k, want := range map[string]string{
		"X-AF-Peer-Kind": "builder",
		"Authorization":  "Bearer token-0123456789abcdef",
		"X-AF-RTE-Name":  "builder-1",
		"X-AF-RTE-Join":  "afj_x",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// TestRedialWait checks that it waits longer each time, ten minutes once refused, a minute on
// another contract.
func TestRedialWait(t *testing.T) {
	if got := RedialWait(1006, 0, time.Second, 0); got != (Redial{Wait: time.Second, Delay: 2 * time.Second}) {
		t.Errorf("a drop: %+v", got)
	}
	if got := RedialWait(1006, 0, 20*time.Second, 0); got.Delay != 30*time.Second {
		t.Errorf("the delay is capped at 30 s: %+v", got)
	}
	if got := RedialWait(1006, 401, 4*time.Second, 0.5); got.Wait != RefusedRedial || !got.Refused {
		t.Errorf("a 401: %+v", got)
	}
	if got := RedialWait(CloseUnauthorized, 0, time.Second, 0); got.Wait != RefusedRedial {
		t.Errorf("an unauthorized close: %+v", got)
	}
	if got := RedialWait(CloseContract, 0, time.Second, 0); got.Wait != ContractRedial {
		t.Errorf("another contract: %+v", got)
	}
}

// TestNoProxy checks that it reaches the hosts NO_PROXY names directly.
func TestNoProxy(t *testing.T) {
	t.Setenv("NO_PROXY", ".internal,forms.example.com")
	for host, want := range map[string]bool{"app.internal": true, "forms.example.com": true, "other.example.com": false} {
		if got := NoProxy(host); got != want {
			t.Errorf("NoProxy(%s) = %v, want %v", host, got, want)
		}
	}
}

// ─── the dial's settings ──────────────────────────────────────────────────────

// TestProxySettings checks no_proxy and https_proxy in lower case, an empty NO_PROXY entry, an
// HTTPS_PROXY that is no address, and a host NO_PROXY names.
func TestProxySettings(t *testing.T) {
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", " , .internal")
	if !NoProxy("App.Internal") || NoProxy("other") {
		t.Error("no_proxy")
	}
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "http://proxy:3128")
	if u := proxyURL("forms.example.com"); u == nil || u.Host != "proxy:3128" {
		t.Errorf("https_proxy: %v", u)
	}
	if proxyURL("app.internal") != nil {
		t.Error("a host no_proxy names went through the proxy")
	}
	t.Setenv("HTTPS_PROXY", "http://[::1")
	if proxyURL("forms.example.com") != nil {
		t.Error("an HTTPS_PROXY that is no address was used")
	}
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	if proxyURL("forms.example.com") != nil {
		t.Error("a proxy without HTTPS_PROXY")
	}
}

// TestTLSConfig checks no check with CONTROLLER_TLS_VERIFY=0, the system's authorities, and
// NODE_EXTRA_CA_CERTS added to them: a file of certificates, one that is none, and a system
// pool that cannot be read.
func TestTLSConfig(t *testing.T) {
	t.Setenv("CONTROLLER_TLS_VERIFY", "0")
	if !tlsConfig().InsecureSkipVerify {
		t.Error("CONTROLLER_TLS_VERIFY=0 still checks")
	}
	t.Setenv("CONTROLLER_TLS_VERIFY", "1")
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	if c := tlsConfig(); c.InsecureSkipVerify || c.RootCAs != nil {
		t.Error("the system's authorities")
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	t.Setenv("NODE_EXTRA_CA_CERTS", file)
	if c := tlsConfig(); c.RootCAs == nil {
		t.Error("NODE_EXTRA_CA_CERTS was not read")
	}
	was := systemCertPool
	systemCertPool = func() (*x509.CertPool, error) { return nil, errors.New("no system pool") }
	t.Cleanup(func() { systemCertPool = was })
	t.Setenv("NODE_EXTRA_CA_CERTS", filepath.Join(t.TempDir(), "none.pem"))
	if c := tlsConfig(); c.RootCAs == nil {
		t.Error("a missing file left no pool")
	}
}

// ─── the dial ─────────────────────────────────────────────────────────────────

// peer is a fake builder: it records what the dial hands it.
type peer struct {
	mu       sync.Mutex
	attached int
	got      []string
	detached int
}

// Attach counts a socket.
func (p *peer) Attach(*Session) { p.mu.Lock(); p.attached++; p.mu.Unlock() }

// Handle records a message's type.
func (p *peer) Handle(_ *Session, m Message) {
	p.mu.Lock()
	p.got = append(p.got, m.Type())
	p.mu.Unlock()
}

// Detach counts a socket gone.
func (p *peer) Detach(*Session) { p.mu.Lock(); p.detached++; p.mu.Unlock() }

// counts reads what it recorded.
func (p *peer) counts() (int, []string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attached, append([]string(nil), p.got...), p.detached
}

// controller starts a fake controller: each dial gets the next of its answers.
//
// Args:
//
//	t: the test.
//	answers: one per dial, the last one for every dial after.
//
// Returns:
//
//	Its address, as CONTROLLER_URL takes it.
func controller(t *testing.T, answers ...func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := min(int(n.Add(1))-1, len(answers)-1)
		answers[i](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// accept takes the socket, says what it is given, then closes it with a code ; 0 : keeps it.
//
// Args:
//
//	msgs: the frames to send.
//	code: the close code, 0 to keep the socket open.
//
// Returns:
//
//	The answer.
func accept(msgs []string, code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for _, m := range msgs {
			_ = c.Write(r.Context(), websocket.MessageText, []byte(m))
		}
		if code != 0 {
			_ = c.Close(websocket.StatusCode(code), "test")
			return
		}
		_, _, _ = c.Read(r.Context())
	}
}

// refuse answers the handshake with a status.
//
// Args:
//
//	status: the HTTP status.
//
// Returns:
//
//	The answer.
func refuse(status int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { http.Error(w, `{"error":"no"}`, status) }
}

// dialEnv sets a dial's environment, with no jitter and the process not stopping.
//
// Args:
//
//	t: the test.
//	url: the controller's address.
func dialEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("CONTROLLER_URL", url)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("LOG_LEVEL", "debug")
	wasStop, wasJitter := isStopping, jitter
	isStopping, jitter = func() bool { return false }, func() float64 { return 0 }
	t.Cleanup(func() { isStopping, jitter = wasStop, wasJitter })
}

// eventually waits for a condition, at most 5 seconds.
//
// Args:
//
//	t: the test.
//	what: what is waited for, for the failure.
//	ok: the condition.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 250; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s in 5 seconds", what)
}

// TestDialFlow checks a refused dial, a socket that opens (its messages handed over) and closes,
// a dial again after a second, and a stop while the socket is open.
func TestDialFlow(t *testing.T) {
	dialEnv(t, controller(t, refuse(503), accept([]string{`{"t":"welcome"}`}, 1000), accept(nil, 0)))
	p := &peer{}
	d, err := DialOut("token-0123456789abcdef", p)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "second socket", func() bool { a, _, _ := p.counts(); return a == 2 })
	if _, got, _ := p.counts(); len(got) != 1 || got[0] != "welcome" {
		t.Errorf("handed %v", got)
	}
	_ = d.Stop()
	_ = d.Stop()
	<-d.Done()
	if _, _, n := p.counts(); n != 2 {
		t.Errorf("%d sockets let go", n)
	}
}

// TestDialRefused checks the long waits: a token refused at the handshake, and a socket the
// controller closes as unauthorized, each stopped while it waits.
func TestDialRefused(t *testing.T) {
	for _, answer := range []func(http.ResponseWriter, *http.Request){refuse(401), accept(nil, CloseUnauthorized)} {
		dialEnv(t, controller(t, answer))
		d, err := DialOut("token-0123456789abcdef", &peer{})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		_ = d.Stop()
		<-d.Done()
	}
}

// TestDialStops checks a stop during the handshake, a process already stopping, a controller
// address that is none, and a dial through a proxy.
func TestDialStops(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		accept(nil, 0)(w, r)
	}
	dialEnv(t, controller(t, slow))
	p := &peer{}
	d, _ := DialOut("token-0123456789abcdef", p)
	time.Sleep(100 * time.Millisecond)
	_ = d.Stop()
	<-d.Done()
	if _, _, n := p.counts(); n != 1 {
		t.Errorf("%d sockets let go", n)
	}

	isStopping = func() bool { return true }
	d, _ = DialOut("token-0123456789abcdef", p)
	<-d.Done()
	isStopping = func() bool { return false }

	t.Setenv("CONTROLLER_URL", "ftp://forms.example.com")
	if _, err := DialOut("token-0123456789abcdef", p); err == nil {
		t.Error("an ftp address was dialed")
	}

	dialEnv(t, "http://forms.example.com")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	d, _ = DialOut("token-0123456789abcdef", p)
	time.Sleep(200 * time.Millisecond)
	_ = d.Stop()
	<-d.Done()
}
