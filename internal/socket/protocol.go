// Package socket speaks the controller's RTE socket, as a builder: the protocol, the sealed
// secrets a build comes with, the spool of a build's items, the session and the dial.
//
// It follows the controller's server/src/rte/socket and server/src/lib at the contract
// SocketContract says. Change it only with the controller's contract: a change here that is not
// one of the controller's breaks the builder against it.
//
//	The RTE socket's protocol: one WebSocket per RTE, opened by the RTE (outbound, as a builder
//	always is) or by the controller (inbound), the same messages both ways whoever dialed. JSON
//	text frames, { t: <type>, ... }.
//
//	RTE -> controller
//	  hello    who it is: id, version, contract, capacity, the jobs it runs and the ones it
//	           still has items for
//	  health   every 20 s: the jobs it runs, its free slots
//	  claimed  it took a job ; refused : it did not (busy, ...)
//	  item     one numbered item of a job: output, started, end, abandon ; kept on its disk
//	           until the controller acks it, resent after a reconnect
//
//	controller -> RTE
//	  welcome  the hello accepted
//	  run      a job (for a builder, a build): its secrets sealed for it
//	  cancel   stop a job
//	  ack      the items of a job stored, up to a number
package socket

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SocketContract is the contract of a socket-mode RTE: the weekly Contract workflow and Follow
// app release compare it with the controller's, from this line, so it stays one line of the
// form const SocketContract = N.
const SocketContract = 3

// AppSocketPath is where the controller takes an RTE's socket (under its BASE_URL).
const AppSocketPath = "/api/v2/rte/socket"

// Why a socket was closed: the reason goes with it, for the log on the other side.
const (
	// CloseUnauthorized : the token, the name or the join token was refused.
	CloseUnauthorized = 4401
	// CloseIDTaken : the RTE's id is another node's, or the RTE of another runner uses it.
	CloseIDTaken = 4403
	// CloseContract : the other side speaks another contract.
	CloseContract = 4409
	// CloseReplaced : a newer socket of the same RTE took over.
	CloseReplaced = 4000
	// CloseLeaseLost : this node no longer holds the RTE's lease.
	CloseLeaseLost = 4001
	// CloseProtocol : a message that could not be read.
	CloseProtocol = 4002
	// CloseGoingAway : the process stops.
	CloseGoingAway = 1001
	// CloseAbnormal : the socket dropped without a close frame.
	CloseAbnormal = 1006
)

// RefusedCloses and RefusedStatuses are the closes and the refusals of the handshake (HTTP
// status) that dialing again soon will not change: the runner was rejected or removed, the token
// or the join token refused, a runner of that name approved already (409). It waits
// RefusedRedial before it dials again, not its usual 1 to 30 seconds: an admin has to act first.
var (
	RefusedCloses   = []int{CloseUnauthorized}
	RefusedStatuses = []int{401, 403, 409}
)

const (
	// RefusedRedial is the wait after a refusal.
	RefusedRedial = 10 * time.Minute
	// ContractRedial is the wait after another contract: the controller or the builder is being
	// upgraded, and the other side usually follows within minutes.
	ContractRedial = time.Minute
	// PingEvery is how often it pings: it keeps proxies from closing an idle socket.
	PingEvery = 20 * time.Second
	// DeadAfter is how long without a word before the socket is dropped.
	DeadAfter = 60 * time.Second
	// Window is how many items of a job may be in flight, not acked yet: beyond it the builder
	// waits for acks.
	Window = 64
	// MaxPayload is a message's size at most.
	MaxPayload = 8 * 1024 * 1024
)

// Message is one message of the socket: its type in t, the rest as the type says.
type Message map[string]any

// Type is the message's type (its t).
//
// Returns:
//
//	The type.
func (m Message) Type() string {
	t, _ := m["t"].(string)
	return t
}

// Encode writes a message as a frame.
//
// Args:
//
//	msg: the message, with its type in t.
//
// Returns:
//
//	The frame's text.
//
// Errors:
//
//	When a value cannot be written as JSON.
func Encode(msg Message) ([]byte, error) {
	return json.Marshal(msg)
}

// Decode reads a frame.
//
// Args:
//
//	data: the frame.
//
// Returns:
//
//	The message, nil when it is not one (not JSON, or no type).
func Decode(data []byte) Message {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil || msg == nil {
		return nil
	}
	if _, ok := msg["t"].(string); !ok {
		return nil
	}
	return msg
}

// SocketURL is the address the builder dials: the controller's address as people know it
// (CONTROLLER_URL, https://forms.example.com, with its BASE_URL if any) as a WebSocket address to
// its socket.
//
// Args:
//
//	appURL: the controller's address (http or https ; ws and wss are taken as they are).
//
// Returns:
//
//	The socket's address (wss://forms.example.com/api/v2/rte/socket).
//
// Errors:
//
//	When it is not an http, https, ws or wss address.
func SocketURL(appURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(appURL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("CONTROLLER_URL must be an http or https address, not %s", appURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
		u.Scheme = strings.ToLower(u.Scheme)
	default:
		return "", fmt.Errorf("CONTROLLER_URL must be an http or https address, not %s", appURL)
	}
	base := strings.TrimRight(u.Path, "/")
	// the socket's own path given already : kept
	if !strings.HasSuffix(base, AppSocketPath) {
		base += AppSocketPath
	}
	u.Path = base
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
