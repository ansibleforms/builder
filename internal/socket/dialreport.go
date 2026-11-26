package socket

// Why the socket to the controller could not be opened, in plain words, and how often the log
// says it: a name that does not resolve, nothing listening, a certificate not trusted, a proxy
// or the controller refusing the handshake each get a warning with a hint, at the default log
// level.
//
// Without flooding: the first failure and every change of cause are said at once, the same
// cause again at most once every DialRepeat while it lasts, and the socket opening after
// failures is said once.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// DialRepeat is how often the same cause, still there, is said again at most.
const DialRepeat = 5 * time.Minute

var (
	credentialsInURL = regexp.MustCompile(`//[^/\s@]+@`)
	htmlTag          = regexp.MustCompile(`<[^>]*>`)
	spaces           = regexp.MustCompile(`\s+`)
	tlsMessage       = regexp.MustCompile(`(?i)SSL routines|wrong version number|first record does not look like a TLS handshake|tls: `)
)

// ─── showing addresses and texts ──────────────────────────────────────────────

// SafeURL shows an address as the log may: no user and password, no query (a token could be in
// either), no fragment.
//
// Args:
//
//	raw: the address.
//
// Returns:
//
//	The address without its credentials, or "?" when it is not one.
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "?"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}

// Scrub shows a text as the log may: the credentials of an address in it (a proxy's
// user:password@) and the given secrets replaced with ***.
//
// Args:
//
//	text: the text (an error's message, the body of a refusal).
//	secrets: the values never to show (RTE_TOKEN, a join token).
//
// Returns:
//
//	The text, scrubbed.
func Scrub(text string, secrets []string) string {
	out := credentialsInURL.ReplaceAllString(text, "//***@")
	for _, s := range secrets {
		if len(s) >= 4 {
			out = strings.ReplaceAll(out, s, "***")
		}
	}
	return out
}

// cut keeps the first n characters of a text.
//
// Args:
//
//	s: the text.
//	n: the characters kept.
//
// Returns:
//
//	The text, cut.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// refusalText reads what the controller said when it refused the handshake: the error of its
// JSON body, else the body's text, cut at 200 characters.
//
// Args:
//
//	body: the body of the refusal.
//
// Returns:
//
//	The reason, "" when there is none.
func refusalText(body string) string {
	raw := strings.TrimSpace(body)
	if raw == "" {
		return ""
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		if e, ok := parsed["error"].(string); ok {
			return cut(e, 200)
		}
		if m, ok := parsed["message"].(string); ok {
			return cut(m, 200)
		}
	}
	// an HTML page of a proxy : its text without the tags
	return cut(strings.TrimSpace(spaces.ReplaceAllString(htmlTag.ReplaceAllString(raw, " "), " ")), 200)
}

// ─── the cause of a failed dial ───────────────────────────────────────────────

// DialFailure is what a failed dial left: the HTTP status the handshake was refused with and its
// body, else the error, else the close code it closed with before it opened.
type DialFailure struct {
	Status int
	Body   string
	Err    error
	Code   int
}

// Why is a failed dial's cause (a short key, the same for the same failure, so its repeats can
// be told apart from a change) and the text the log shows.
type Why struct {
	Cause string
	Text  string
}

// errorCode names a dial error the way the hints know it (the codes Node.js gave the same
// failures, so the causes stay the ones the controller's log uses).
//
// Args:
//
//	err: the dial's error.
//
// Returns:
//
//	The code, "" when it is none of the known ones.
func errorCode(err error) string {
	var (
		dnsErr  *net.DNSError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		invalid x509.CertificateInvalidError
		record  tls.RecordHeaderError
		verify  *tls.CertificateVerificationError
	)
	switch {
	case errors.As(err, &unknown):
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	case errors.As(err, &host):
		return "ERR_TLS_CERT_ALTNAME_INVALID"
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return "CERT_HAS_EXPIRED"
	case errors.As(err, &invalid):
		return "CERT_UNTRUSTED"
	case errors.As(err, &verify):
		return "CERT_UNTRUSTED"
	case errors.As(err, &record):
		return "EPROTO"
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return "ENOTFOUND"
		}
		return "EAI_AGAIN"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "ECONNREFUSED"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "EHOSTUNREACH"
	case errors.Is(err, syscall.ENETUNREACH):
		return "ENETUNREACH"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET"
	case errors.Is(err, syscall.EPIPE):
		return "EPIPE"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "ETIMEDOUT"
	}
	return ""
}

// DescribeDialFailure says why a dial to the controller failed, in plain words, with a hint for
// the common causes.
//
// Args:
//
//	f: what the dial left.
//	dialed: the address dialed.
//	proxy: true when it went through HTTPS_PROXY.
//	secrets: the values never to show.
//
// Returns:
//
//	Its cause and its text.
func DescribeDialFailure(f DialFailure, dialed string, proxy bool, secrets []string) Why {
	host := "?"
	if u, err := url.Parse(dialed); err == nil && u.Host != "" {
		host = u.Host
	}
	say := func(cause, text string) Why { return Why{cause, Scrub(text, secrets)} }

	// a proxy that refused the CONNECT : Go says its status in the error, not as a response
	if f.Status == 0 && f.Err != nil && strings.Contains(f.Err.Error(), "Proxy Authentication Required") {
		f.Status = 407
	}

	// ─── the controller (or a proxy before it) answered, but not with a socket ───
	if f.Status != 0 {
		said := fmt.Sprintf("HTTP %d", f.Status)
		if why := refusalText(f.Body); why != "" {
			said += ": " + why
		}
		cause := fmt.Sprintf("http-%d", f.Status)
		switch f.Status {
		case 401:
			return say(cause, fmt.Sprintf("the controller refused this runner's token (%s): check RTE_TOKEN against its runner's token, or RTE_JOIN_TOKEN for a first join", said))
		case 403:
			return say(cause, fmt.Sprintf("the controller refused this runner (%s): its runner may be rejected or removed under Connections > Runners", said))
		case 404:
			return say(cause, fmt.Sprintf("no runner socket at this address (%s): CONTROLLER_URL must be the controller's address as people open it, with its BASE_URL path if any, and a proxy in front must pass it on", said))
		case 407:
			return say(cause, fmt.Sprintf("the proxy wants credentials (%s): give them in HTTPS_PROXY (http://user:password@proxy:port)", said))
		case 409:
			return say(cause, fmt.Sprintf("the controller refused this runner (%s): a runner of that name (RTE_NAME) exists already or waits for approval", said))
		case 400, 426:
			return say(cause, fmt.Sprintf("the socket was not let through (%s): a proxy or load balancer in front must pass WebSocket upgrades", said))
		case 502, 503, 504:
			return say(cause, fmt.Sprintf("a proxy or load balancer answered instead (%s): it cannot reach the app behind it, or does not pass WebSocket upgrades", said))
		}
		return say(cause, fmt.Sprintf("the app refused the socket (%s)", said))
	}

	if f.Err == nil {
		code := "?"
		if f.Code != 0 {
			code = fmt.Sprint(f.Code)
		}
		return say("close-"+code, fmt.Sprintf("the socket closed before it opened (%s)", code))
	}
	code := errorCode(f.Err)
	message := f.Err.Error()
	via := ""
	if proxy {
		via = " (through the proxy, HTTPS_PROXY)"
	}

	// ─── the time ───
	if errors.Is(f.Err, context.DeadlineExceeded) || (code == "" && os.IsTimeout(f.Err)) {
		return say("net-timeout", fmt.Sprintf("no answer from %s%s in time (the handshake has timed out): a firewall dropping the traffic, or a proxy holding the upgrade?", host, via))
	}

	// ─── the certificate ───
	switch code {
	case "UNABLE_TO_VERIFY_LEAF_SIGNATURE", "CERT_UNTRUSTED":
		return say("tls-"+code, fmt.Sprintf("the controller's certificate is not trusted (%s: %s): name its CA with NODE_EXTRA_CA_CERTS (a PEM file), or set CONTROLLER_TLS_VERIFY=0 inside one trusted network", code, message))
	case "ERR_TLS_CERT_ALTNAME_INVALID":
		return say("tls-"+code, fmt.Sprintf("the controller's certificate is not for %s (%s): CONTROLLER_URL must use a name the certificate holds, or set CONTROLLER_TLS_VERIFY=0 inside one trusted network", host, message))
	case "CERT_HAS_EXPIRED":
		return say("tls-"+code, fmt.Sprintf("the app's certificate is out of its dates (%s: %s): renew it, and check the clocks", code, message))
	}
	if code == "EPROTO" || (code == "" && tlsMessage.MatchString(message)) {
		c := code
		if c == "" {
			c = "other"
		}
		prefix := ""
		if code != "" {
			prefix = code + ": "
		}
		return say("tls-"+c, fmt.Sprintf("the TLS handshake failed%s (%s%s): an https address to a port that speaks plain http (or the other way round)?", via, prefix, message))
	}

	// ─── the network ───
	switch code {
	case "ENOTFOUND", "EAI_AGAIN":
		name := host
		var dnsErr *net.DNSError
		if errors.As(f.Err, &dnsErr) && dnsErr.Name != "" {
			name = dnsErr.Name
		}
		return say("dns-"+code, fmt.Sprintf("the name %s does not resolve%s (%s): check the CONTROLLER_URL host and this container's DNS", name, via, code))
	case "ECONNREFUSED":
		what := "app"
		if proxy {
			what = "proxy"
		}
		return say("net-"+code, fmt.Sprintf("nothing listens at %s%s (%s): is the %s running, on that port?", host, via, code, what))
	case "EHOSTUNREACH", "ENETUNREACH", "ETIMEDOUT", "ECONNRESET", "EPIPE":
		return say("net-"+code, fmt.Sprintf("the network did not get through to %s%s (%s: %s): a firewall, a route or a proxy in the way?", host, via, code, message))
	}
	return say("other-"+cut(message, 60), message+via)
}

// ─── how often the log says it ────────────────────────────────────────────────

// DialReport counts the failed dials in a row, and tells when the log says one.
type DialReport struct {
	// RepeatEvery is the least time between two logs of the same cause.
	RepeatEvery time.Duration
	// Now is the clock (time.Now ; the tests set their own).
	Now func() time.Time
	// cause is the cause of the failures in a row, "" when the last dial opened.
	cause string
	// failures counts the failures in a row (any cause), since is when the first of them came,
	// loggedAt when the log last spoke.
	failures int
	since    time.Time
	loggedAt time.Time
}

// NewDialReport makes a report that repeats a cause every DialRepeat at most.
//
// Returns:
//
//	The report.
func NewDialReport() *DialReport {
	return &DialReport{RepeatEvery: DialRepeat, Now: time.Now}
}

// failed counts a failed dial.
//
// Args:
//
//	cause: its cause.
//
// Returns:
//
//	Whether the log says it now, whether it is the same cause as the failure before, the
//	failures in a row, and when the first of them came.
func (r *DialReport) failed(cause string) (log, repeated bool, failures int, since time.Time) {
	now := r.Now()
	if r.failures == 0 {
		r.since = now
	}
	r.failures++
	repeated = cause == r.cause
	if !repeated || now.Sub(r.loggedAt) >= r.RepeatEvery {
		log = true
		r.loggedAt = now
	}
	r.cause = cause
	return log, repeated, r.failures, r.since
}

// Connected counts a dial that opened.
//
// Returns:
//
//	The failures in a row before it and when the first came (the log says it is back) ; 0
//	failures when there were none.
func (r *DialReport) Connected() (failures int, since time.Time) {
	failures, since = r.failures, r.since
	r.cause, r.failures, r.since, r.loggedAt = "", 0, time.Time{}, time.Time{}
	return failures, since
}

// Ago says how long ago, in words: "42 seconds", "7 minutes", "3 hours".
//
// Args:
//
//	d: the time.
//
// Returns:
//
//	The words.
func Ago(d time.Duration) string {
	s := max(0, int(d.Round(time.Second).Seconds()))
	switch {
	case s < 120:
		return fmt.Sprintf("%d seconds", s)
	case s < 7200:
		return fmt.Sprintf("%d minutes", (s+30)/60)
	}
	return fmt.Sprintf("%d hours", (s+1800)/3600)
}

// FailureLine is the line the log shows for a failed dial.
//
// Args:
//
//	r: the report.
//	why: the failure's cause and text.
//	prefix: who dials what ("Builder 'x': cannot reach the controller at wss://...").
//
// Returns:
//
//	The line, "" when the log stays quiet (the same cause said less than RepeatEvery ago).
func FailureLine(r *DialReport, why Why, prefix string) string {
	log, repeated, failures, since := r.failed(why.Cause)
	if !log {
		return ""
	}
	still := ""
	if repeated {
		still = fmt.Sprintf(" (still, %d failed dials in %s)", failures, Ago(r.Now().Sub(since)))
	}
	return fmt.Sprintf("%s: %s%s", prefix, why.Text, still)
}
