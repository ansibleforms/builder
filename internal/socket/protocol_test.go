package socket

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestContractIsTheController checks the contract against the controller's latest release, when
// CONTROLLER_PROTOCOL_FILE names its protocol.js (the weekly Contract workflow fetches it).
func TestContractIsTheController(t *testing.T) {
	file := os.Getenv("CONTROLLER_PROTOCOL_FILE")
	if file == "" {
		t.Skip("CONTROLLER_PROTOCOL_FILE is not set")
	}
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export const SOCKET_CONTRACT = (\d+);`).FindSubmatch(text)
	if m == nil {
		t.Fatal("the controller's protocol.js has no SOCKET_CONTRACT")
	}
	theirs, _ := strconv.Atoi(string(m[1]))
	if theirs != SocketContract {
		t.Fatalf("the controller speaks contract %d, the builder %d: release a builder that speaks it", theirs, SocketContract)
	}
}

// TestSocketURL checks the address it dials.
func TestSocketURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://forms.example.com":                    "wss://forms.example.com/api/v2/rte/socket",
		"http://forms.example.com:8080/af/":            "ws://forms.example.com:8080/af/api/v2/rte/socket",
		"wss://forms.example.com/af/api/v2/rte/socket": "wss://forms.example.com/af/api/v2/rte/socket",
		" https://forms.example.com/?x=1#y ":           "wss://forms.example.com/api/v2/rte/socket",
	} {
		got, err := SocketURL(in)
		if err != nil || got != want {
			t.Errorf("SocketURL(%q) = %q, %v ; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "forms.example.com", "ftp://forms.example.com"} {
		if _, err := SocketURL(bad); err == nil {
			t.Errorf("SocketURL(%q) took it", bad)
		}
	}
}

// TestDecode checks that a frame without a type is no message.
func TestDecode(t *testing.T) {
	if m := Decode([]byte(`{"t":"ack","seq":3}`)); m == nil || m.Type() != "ack" {
		t.Errorf("an ack: %v", m)
	}
	for _, bad := range []string{`{"seq":3}`, `[1]`, `nope`, `null`} {
		if Decode([]byte(bad)) != nil {
			t.Errorf("%s read as a message", bad)
		}
	}
}
