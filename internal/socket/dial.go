package socket

// The builder's one socket to the controller: dialed (outbound only : the controller never dials
// a builder), and dialed again when it drops, after RedialWait. As the AnsibleForms runner's
// (server/src/rte/socket/rte-socket.js dialOut), with the builder's kind in its headers so the
// controller keeps it as a builder, never a runner.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ansibleforms/builder/internal/logger"
	"github.com/ansibleforms/builder/internal/settings"
	"github.com/ansibleforms/builder/internal/shutdown"
	"github.com/ansibleforms/builder/internal/version"
)

// handshakeTimeout is how long a dial may take, its TLS and its upgrade included.
const handshakeTimeout = 15 * time.Second

// BuilderHeaders is what the builder says of itself when it dials: its token, its id and name,
// its host and version (shown to the admin who approves it), that it is a builder, and its join
// token when it has one.
//
// Args:
//
//	token: RTE_TOKEN.
//
// Returns:
//
//	The headers.
func BuilderHeaders(token string) http.Header {
	host, _ := os.Hostname()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("X-AF-RTE-Id", settings.NodeID())
	h.Set("X-AF-RTE-Name", settings.BuilderName())
	h.Set("X-AF-RTE-Host", host)
	h.Set("X-AF-RTE-Version", version.App)
	h.Set("X-AF-Peer-Kind", "builder")
	if j := settings.JoinToken(); j != "" {
		h.Set("X-AF-RTE-Join", j)
	}
	return h
}

// Redial is how long the builder waits before it dials again.
type Redial struct {
	// Wait is the wait before this dial.
	Wait time.Duration
	// Delay is the base of the next ordinary wait.
	Delay time.Duration
	// Refused tells that an admin or an upgrade has to act first.
	Refused bool
}

// RedialWait says how long it waits before it dials again: a little longer each time (1 s up to
// 30 s, with some jitter) ; refused (rejected, removed, a token or join token refused)
// RefusedRedial ; another contract (an upgrade in progress) ContractRedial.
//
// Args:
//
//	code: the close code.
//	status: the HTTP status of a refused handshake, 0 for none.
//	delay: the current base wait.
//	random: a number in [0, 1), the jitter.
//
// Returns:
//
//	The wait.
func RedialWait(code, status int, delay time.Duration, random float64) Redial {
	if slices.Contains(RefusedCloses, code) || slices.Contains(RefusedStatuses, status) {
		return Redial{Wait: RefusedRedial, Delay: time.Second, Refused: true}
	}
	if code == CloseContract {
		return Redial{Wait: ContractRedial, Delay: time.Second, Refused: true}
	}
	jitter := time.Duration(math.Floor(random*1000)) * time.Millisecond
	return Redial{Wait: delay + jitter, Delay: min(delay*2, 30*time.Second)}
}

// NoProxy tells whether NO_PROXY says to reach a host directly.
//
// Args:
//
//	host: the controller's host name.
//
// Returns:
//
//	True when NO_PROXY names it (or *).
func NoProxy(host string) bool {
	list := os.Getenv("NO_PROXY")
	if list == "" {
		list = os.Getenv("no_proxy")
	}
	host = strings.ToLower(host)
	for _, p := range strings.Split(list, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		suffix := p
		if !strings.HasPrefix(p, ".") {
			suffix = "." + p
		}
		if p == "*" || host == p || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// proxyURL is the proxy it dials through: HTTPS_PROXY, for ws and wss alike, unless NO_PROXY
// names the controller.
//
// Args:
//
//	host: the controller's host name.
//
// Returns:
//
//	The proxy, nil for none.
func proxyURL(host string) *url.URL {
	raw := os.Getenv("HTTPS_PROXY")
	if raw == "" {
		raw = os.Getenv("https_proxy")
	}
	if raw == "" || NoProxy(host) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		logger.Warning("HTTPS_PROXY is no address (%s): dialing the controller directly", SafeURL(raw))
		return nil
	}
	return u
}

// systemCertPool reads the system's authorities ; the tests make it fail.
var systemCertPool = x509.SystemCertPool

// tlsConfig is how it checks the controller's certificate: the system's authorities and the
// ones of NODE_EXTRA_CA_CERTS (a PEM file, as the Node.js builder took), or no check with
// CONTROLLER_TLS_VERIFY=0.
//
// Returns:
//
//	The TLS configuration.
func tlsConfig() *tls.Config {
	if !settings.ControllerTLSVerify() {
		return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // CONTROLLER_TLS_VERIFY=0, asked for
	}
	file := os.Getenv("NODE_EXTRA_CA_CERTS")
	if file == "" {
		return &tls.Config{}
	}
	pool, err := systemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pem, err := os.ReadFile(file)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		logger.Warning("NODE_EXTRA_CA_CERTS: no certificate read from %s", file)
	}
	return &tls.Config{RootCAs: pool}
}

// Peer is what takes the socket once it is open: the builder.
type Peer interface {
	// Attach takes a socket that just opened (it says hello on it).
	Attach(s *Session)
	// Handle takes each message the socket reads, in order.
	Handle(s *Session, msg Message)
	// Detach lets go of a socket that is gone.
	Detach(s *Session)
}

// Dialer keeps the socket to the controller open.
type Dialer struct {
	token string
	peer  Peer
	mu    sync.Mutex
	// current is the open session, nil between two dials.
	current *Session
	stopped bool
	stop    chan struct{}
	// done closes once its loop has ended.
	done chan struct{}
}

// isStopping tells that the process stops, and jitter gives the redial's jitter ; the tests
// replace both.
var (
	isStopping = shutdown.IsStopping
	jitter     = rand.Float64
)

// DialOut starts keeping the socket to the controller open, in its own goroutine.
//
// Args:
//
//	token: RTE_TOKEN.
//	peer: takes the socket once it is open.
//
// Returns:
//
//	The dialer ; its Stop stops dialing and closes the socket.
//
// Errors:
//
//	When CONTROLLER_URL is no address.
func DialOut(token string, peer Peer) (*Dialer, error) {
	target, err := SocketURL(settings.ControllerURL())
	if err != nil {
		return nil, err
	}
	d := &Dialer{token: token, peer: peer, stop: make(chan struct{}), done: make(chan struct{})}
	go d.loop(target)
	return d, nil
}

// Stop stops dialing and drops the socket.
//
// Returns:
//
//	Nil.
func (d *Dialer) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.stopped {
		d.stopped = true
		close(d.stop)
	}
	if d.current != nil {
		d.current.Close(CloseGoingAway, "the builder stops")
	}
	return nil
}

// loop dials, hands the socket over, and dials again once it is gone.
//
// Args:
//
//	target: the socket's address.
func (d *Dialer) loop(target string) {
	defer close(d.done)
	u, _ := url.Parse(target)
	proxy := proxyURL(u.Hostname())
	transport := &http.Transport{TLSClientConfig: tlsConfig(), Proxy: func(*http.Request) (*url.URL, error) { return proxy, nil }}
	client := &http.Client{Transport: transport}
	shown := SafeURL(target)
	report := NewDialReport()
	secrets := []string{d.token, settings.JoinToken()}
	name := settings.BuilderName()
	through := ""
	if proxy != nil {
		through = " through the proxy"
	}
	logger.Notice("Builder '%s': dialing the controller at %s%s", name, shown, through)
	delay := time.Second
	for {
		if isStopping() || d.isStopped() {
			return
		}
		// step 1 : the dial
		ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
		conn, res, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client, HTTPHeader: BuilderHeaders(d.token)})
		cancel()
		opened := err == nil
		status, code := 0, 0
		var failure DialFailure
		if err != nil {
			failure.Err = err
			if res != nil && res.StatusCode != http.StatusSwitchingProtocols {
				status = res.StatusCode
				body, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
				failure = DialFailure{Status: status, Body: string(body)}
			}
		} else {
			// step 2 : the socket, read until it is gone
			delay = time.Second
			if failures, since := report.Connected(); failures > 0 {
				logger.Notice("Builder '%s': reached the controller at %s again, after %d failed dial(s) in %s", name, shown, failures, Ago(time.Since(since)))
			}
			s := NewSession(conn, "app")
			d.mu.Lock()
			d.current = s
			stopped := d.stopped
			d.mu.Unlock()
			if stopped {
				s.Close(CloseGoingAway, "the builder stops")
			}
			code = d.run(s)
			d.mu.Lock()
			d.current = nil
			d.mu.Unlock()
		}
		if isStopping() || d.isStopped() {
			return
		}
		// step 3 : the wait before the next dial, and what the log says of it
		next := RedialWait(code, status, delay, jitter())
		delay = next.Delay
		again := fmt.Sprintf("%d seconds", int(math.Round(next.Wait.Seconds())))
		if next.Wait >= time.Minute {
			again = fmt.Sprintf("%d minute(s)", int(math.Round(next.Wait.Minutes())))
		}
		switch {
		case !opened:
			why := DescribeDialFailure(failure, target, proxy != nil, secrets)
			if line := FailureLine(report, why, fmt.Sprintf("Builder '%s': cannot reach the controller at %s", name, shown)); line != "" {
				logger.Warning("%s: dialing again in %s", line, again)
			}
		case next.Refused:
			logger.Warning("Builder '%s': the controller refused this builder (%d): dialing again in %s, or restart it once an admin acted", name, code, again)
		default:
			logger.Info("Builder: dialing the controller again in %s", again)
		}
		select {
		case <-d.stop:
			return
		case <-time.After(next.Wait):
		}
	}
}

// run hands an open socket to the builder and reads it until it is gone.
//
// Args:
//
//	s: the session.
//
// Returns:
//
//	Its close code.
func (d *Dialer) run(s *Session) int {
	d.peer.Attach(s)
	defer d.peer.Detach(s)
	return s.Run(func(msg Message) { d.peer.Handle(s, msg) })
}

// Done closes once the dialer has stopped dialing, after Stop.
//
// Returns:
//
//	The channel.
func (d *Dialer) Done() <-chan struct{} { return d.done }

// isStopped tells whether Stop was called.
//
// Returns:
//
//	True once stopped.
func (d *Dialer) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}
