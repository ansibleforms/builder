package socket

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDescribeDialFailure checks a few common failures' causes and hints, secrets scrubbed.
func TestDescribeDialFailure(t *testing.T) {
	target := "wss://forms.example.com/api/v2/rte/socket"
	why := DescribeDialFailure(DialFailure{Status: 401, Body: `{"error":"bad token secret-token-123"}`}, target, false, []string{"secret-token-123"})
	if why.Cause != "http-401" || regexp.MustCompile(`secret-token-123`).MatchString(why.Text) {
		t.Errorf("401: %+v", why)
	}
	refused := &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	if why := DescribeDialFailure(DialFailure{Err: refused}, target, false, nil); why.Cause != "net-ECONNREFUSED" {
		t.Errorf("refused: %+v", why)
	}
	dns := &net.DNSError{Name: "forms.example.com", Err: "no such host", IsNotFound: true}
	if why := DescribeDialFailure(DialFailure{Err: dns}, target, true, nil); why.Cause != "dns-ENOTFOUND" {
		t.Errorf("dns: %+v", why)
	}
	if why := DescribeDialFailure(DialFailure{Err: errors.New("Proxy Authentication Required")}, target, true, nil); why.Cause != "http-407" {
		t.Errorf("proxy: %+v", why)
	}
	if got := SafeURL("https://u:p@proxy.example.com:3128/x?token=1#f"); got != "https://proxy.example.com:3128/x" {
		t.Errorf("SafeURL: %s", got)
	}
	if got := Scrub("via http://u:p@proxy:3128", nil); got != "via http://***@proxy:3128" {
		t.Errorf("Scrub: %s", got)
	}
}

// TestDialReport checks that a cause is said once, then again only after RepeatEvery.
func TestDialReport(t *testing.T) {
	now := time.Unix(0, 0)
	r := &DialReport{RepeatEvery: time.Minute, Now: func() time.Time { return now }}
	why := Why{Cause: "net-x", Text: "down"}
	if FailureLine(r, why, "B") == "" {
		t.Error("the first failure is quiet")
	}
	now = now.Add(10 * time.Second)
	if FailureLine(r, why, "B") != "" {
		t.Error("the same cause is said again at once")
	}
	now = now.Add(time.Minute)
	if line := FailureLine(r, why, "B"); line != "B: down (still, 3 failed dials in 70 seconds)" {
		t.Errorf("the repeat: %q", line)
	}
	if n, _ := r.Connected(); n != 3 {
		t.Errorf("connected after %d failures", n)
	}
	if Ago(3*time.Hour) != "3 hours" || Ago(7*time.Minute) != "7 minutes" {
		t.Error("Ago")
	}
}

// TestEveryCause checks the cause each failure gets: every refusal status, the certificate's
// troubles, the network's, a timeout, a close before opening, and the rest.
func TestEveryCause(t *testing.T) {
	target := "wss://forms.example.com/api/v2/rte/socket"
	op := func(err error) error {
		return &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: err}}
	}
	cases := []struct {
		f    DialFailure
		want string
	}{
		{DialFailure{Status: 403}, "http-403"},
		{DialFailure{Status: 404, Body: "<html><b>Not</b> found</html>"}, "http-404"},
		{DialFailure{Status: 407, Body: `{"message":"auth"}`}, "http-407"},
		{DialFailure{Status: 409}, "http-409"},
		{DialFailure{Status: 400}, "http-400"},
		{DialFailure{Status: 502}, "http-502"},
		{DialFailure{Status: 500, Body: "  "}, "http-500"},
		{DialFailure{}, "close-?"},
		{DialFailure{Code: 1006}, "close-1006"},
		{DialFailure{Err: context.DeadlineExceeded}, "net-timeout"},
		{DialFailure{Err: x509.UnknownAuthorityError{}}, "tls-UNABLE_TO_VERIFY_LEAF_SIGNATURE"},
		{DialFailure{Err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "x"}}, "tls-ERR_TLS_CERT_ALTNAME_INVALID"},
		{DialFailure{Err: x509.CertificateInvalidError{Reason: x509.Expired}}, "tls-CERT_HAS_EXPIRED"},
		{DialFailure{Err: x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}}, "tls-CERT_UNTRUSTED"},
		{DialFailure{Err: &tls.CertificateVerificationError{Err: errors.New("bad")}}, "tls-CERT_UNTRUSTED"},
		{DialFailure{Err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}}, "tls-EPROTO"},
		{DialFailure{Err: errors.New("remote error: tls: handshake failure")}, "tls-other"},
		{DialFailure{Err: &net.DNSError{Name: "x", IsTemporary: true}}, "dns-EAI_AGAIN"},
		{DialFailure{Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, "dns-ENOTFOUND"},
		{DialFailure{Err: op(syscall.EHOSTUNREACH)}, "net-EHOSTUNREACH"},
		{DialFailure{Err: op(syscall.ENETUNREACH)}, "net-ENETUNREACH"},
		{DialFailure{Err: op(syscall.ECONNRESET)}, "net-ECONNRESET"},
		{DialFailure{Err: op(syscall.EPIPE)}, "net-EPIPE"},
		{DialFailure{Err: op(syscall.ETIMEDOUT)}, "net-ETIMEDOUT"},
		{DialFailure{Err: errors.New("something else")}, "other-something else"},
	}
	for _, c := range cases {
		if why := DescribeDialFailure(c.f, target, true, nil); why.Cause != c.want {
			t.Errorf("%+v: cause %s, want %s (%s)", c.f, why.Cause, c.want, why.Text)
		}
	}
	if why := DescribeDialFailure(DialFailure{Err: op(syscall.ECONNREFUSED)}, target, true, nil); !strings.Contains(why.Text, "is the proxy running") {
		t.Errorf("refused through the proxy: %s", why.Text)
	}
	if why := DescribeDialFailure(DialFailure{Status: 401}, "::", false, nil); !strings.Contains(why.Text, "RTE_TOKEN") {
		t.Errorf("an address that is none: %s", why.Text)
	}
}

// TestTexts checks the helpers of the texts: an address that is none, a long text cut, and the
// reason of a refusal.
func TestTexts(t *testing.T) {
	if SafeURL("not an address") != "?" || SafeURL("%") != "?" {
		t.Error("SafeURL of no address")
	}
	if cut("abcdef", 3) != "abc" || cut("ab", 3) != "ab" {
		t.Error("cut")
	}
	for body, want := range map[string]string{
		"":                        "",
		`{"error":"refused"}`:     "refused",
		`{"message":"go away"}`:   "go away",
		`{"other":1}`:             `{"other":1}`,
		"<p>Bad   <i>gateway</i>": "Bad gateway",
	} {
		if got := refusalText(body); got != want {
			t.Errorf("refusalText(%q) = %q, want %q", body, got, want)
		}
	}
	if r := NewDialReport(); r.RepeatEvery != DialRepeat || r.Now == nil {
		t.Error("NewDialReport")
	}
}
