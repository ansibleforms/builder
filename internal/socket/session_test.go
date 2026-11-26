package socket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pair opens a socket to a test server and hands both ends over.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The client's session, and the server's end.
func pair(t *testing.T) (*Session, *websocket.Conn) {
	t.Helper()
	ends := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ends <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewSession(conn, "test"), <-ends
}

// TestSessionMessages checks that messages go out in order and come in to the handler, and
// that Run returns the close code the other side sent.
func TestSessionMessages(t *testing.T) {
	s, peer := pair(t)
	got := make(chan Message, 4)
	codes := make(chan int, 1)
	go func() { codes <- s.Run(func(m Message) { got <- m }) }()

	// out : in order
	for i := 1; i <= 3; i++ {
		if !s.Send(Message{"t": "item", "n": i}) {
			t.Fatal("a send was refused")
		}
	}
	for i := 1; i <= 3; i++ {
		_, data, err := peer.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if m := Decode(data); m["n"].(float64) != float64(i) {
			t.Fatalf("message %d came as %v", i, m)
		}
	}

	// in : to the handler
	_ = peer.Write(context.Background(), websocket.MessageText, []byte(`{"t":"ack","seq":5}`))
	select {
	case m := <-got:
		if m.Type() != "ack" {
			t.Errorf("got %v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the message did not reach the handler")
	}

	// the other side closes : its code comes back, nothing more is sent
	_ = peer.Close(websocket.StatusCode(CloseContract), "another contract")
	select {
	case code := <-codes:
		if code != CloseContract {
			t.Errorf("closed with %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if s.Open() || s.Send(Message{"t": "health"}) {
		t.Error("the session still sends once closed")
	}
}

// TestSessionUnreadable checks that a frame that is no message closes the socket as a protocol
// error.
func TestSessionUnreadable(t *testing.T) {
	s, peer := pair(t)
	go s.Run(func(Message) { t.Error("an unreadable frame reached the handler") })
	_ = peer.Write(context.Background(), websocket.MessageText, []byte(`{"no":"type"}`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := peer.Read(ctx)
	if code := websocket.CloseStatus(err); int(code) != CloseProtocol {
		t.Errorf("closed with %d (%v)", code, err)
	}
}

// TestSessionSendRefused checks a message that is no JSON, and a full queue.
func TestSessionSendRefused(t *testing.T) {
	s, _ := pair(t)
	if s.Send(Message{"t": "x", "bad": func() {}}) {
		t.Error("a message that is no JSON was sent")
	}
	// nothing writes (no Run) : the queue fills
	for i := 0; i < sendQueue; i++ {
		s.Send(Message{"t": "x"})
	}
	if s.Send(Message{"t": "x"}) {
		t.Error("a send went past a full queue")
	}
	s.Terminate()
}

// TestSessionDropped checks a peer gone without a close (abnormal), a long reason cut, and a
// write that fails.
func TestSessionDropped(t *testing.T) {
	s, peer := pair(t)
	codes := make(chan int, 1)
	go func() { codes <- s.Run(func(Message) {}) }()
	_ = peer.CloseNow()
	if code := <-codes; code != CloseAbnormal {
		t.Errorf("dropped with %d", code)
	}

	s, _ = pair(t)
	s.Close(CloseGoingAway, strings.Repeat("x", 200))
	s.Close(CloseGoingAway, "again")
	<-s.ctx.Done()

	// a frame queued on a socket already gone : its write fails and drops the session
	s, _ = pair(t)
	_ = s.conn.CloseNow()
	s.out <- []byte(`{"t":"x"}`)
	s.write()
	if s.Open() {
		t.Error("a failed write left it open")
	}
}

// TestSessionPing checks that a ping answered keeps the socket, and silence drops it.
func TestSessionPing(t *testing.T) {
	s, peer := pair(t)
	s.pingEvery, s.deadAfter = 20*time.Millisecond, time.Hour
	// the peer reads, so it answers the pings
	go func() {
		for {
			if _, _, err := peer.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	codes := make(chan int, 1)
	go func() { codes <- s.Run(func(Message) {}) }()
	before := s.lastHeard.Load()
	time.Sleep(150 * time.Millisecond)
	if s.lastHeard.Load() == before {
		t.Error("an answered ping was not heard")
	}
	s.Terminate()
	<-codes

	// silence : nothing heard for longer than deadAfter, set before it runs
	quiet, _ := pair(t)
	quiet.pingEvery, quiet.deadAfter = 20*time.Millisecond, time.Nanosecond
	done := make(chan int, 1)
	go func() { done <- quiet.Run(func(Message) {}) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a silent peer was kept")
	}
}
