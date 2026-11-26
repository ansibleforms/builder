package socket

// One socket to the controller: JSON messages in and out, a ping every 20 s that keeps proxies
// from closing it, and the socket dropped when nothing was heard for 60 s (a peer gone without a
// word).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/ansibleforms/builder/internal/logger"
)

// sendQueue is how many frames may wait to be written: a send beyond it is refused, and the
// spool sends the item again later.
const sendQueue = 1024

// Session is an open socket's session. Its Send and Close may be called from any goroutine.
type Session struct {
	conn  *websocket.Conn
	label string
	// out holds the frames waiting to be written, in order.
	out chan []byte
	// ctx ends when the socket is gone.
	ctx    context.Context
	cancel context.CancelFunc
	// pingEvery and deadAfter are PingEvery and DeadAfter ; the tests shorten them.
	pingEvery, deadAfter time.Duration
	// lastHeard is when the other side last said something (unix nanoseconds).
	lastHeard atomic.Int64
	closed    atomic.Bool
	closeOnce sync.Once
}

// NewSession takes an open socket.
//
// Args:
//
//	conn: the open socket.
//	label: who is on the other side, for the log.
//
// Returns:
//
//	The session ; Run reads it.
func NewSession(conn *websocket.Conn, label string) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{conn: conn, label: label, out: make(chan []byte, sendQueue), ctx: ctx, cancel: cancel, pingEvery: PingEvery, deadAfter: DeadAfter}
	s.lastHeard.Store(time.Now().UnixNano())
	conn.SetReadLimit(MaxPayload)
	return s
}

// Open tells whether messages can be sent.
//
// Returns:
//
//	True until the socket is closed.
func (s *Session) Open() bool { return !s.closed.Load() }

// Send queues a message to be written.
//
// Args:
//
//	msg: the message, its type in t.
//
// Returns:
//
//	True when queued, false when the socket is not open or its queue is full (the caller's own
//	logic sends it again or drops it).
func (s *Session) Send(msg Message) bool {
	if !s.Open() {
		return false
	}
	data, err := Encode(msg)
	if err != nil {
		logger.Error("Runner socket (%s): a message that is no JSON: %v", s.label, err)
		return false
	}
	select {
	case s.out <- data:
		return true
	default:
		return false
	}
}

// Close closes the socket, with a code and a reason for the other side's log. A peer that does
// not answer the close is dropped after 5 seconds.
//
// Args:
//
//	code: the close code (protocol.go's Close constants).
//	reason: why.
func (s *Session) Close(code int, reason string) {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		go func() {
			_ = s.conn.Close(websocket.StatusCode(code), reason)
			s.cancel()
		}()
	})
}

// Terminate drops the socket at once, without a close handshake.
func (s *Session) Terminate() {
	s.closed.Store(true)
	_ = s.conn.CloseNow()
	s.cancel()
}

// Run reads the socket until it is gone, each message handed to onMessage in order, and keeps
// it alive with pings meanwhile.
//
// Args:
//
//	onMessage: called with every message read, from Run's goroutine.
//
// Returns:
//
//	The close code (CloseAbnormal when it dropped without one).
func (s *Session) Run(onMessage func(Message)) int {
	go s.write()
	go s.ping()
	defer func() {
		s.closed.Store(true)
		s.cancel()
	}()
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			code := int(websocket.CloseStatus(err))
			if code == -1 {
				code = CloseAbnormal
				if !errors.Is(err, context.Canceled) {
					logger.Debug("Runner socket (%s): %v", s.label, err)
				}
			}
			_ = s.conn.CloseNow()
			return code
		}
		s.lastHeard.Store(time.Now().UnixNano())
		msg := Decode(data)
		if msg == nil {
			logger.Warning("Runner socket (%s): a message that is not one, closing", s.label)
			s.Close(CloseProtocol, "unreadable message")
			continue
		}
		onMessage(msg)
	}
}

// write writes the queued frames, in order, until the socket is gone.
func (s *Session) write() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case data := <-s.out:
			if err := s.conn.Write(s.ctx, websocket.MessageText, data); err != nil {
				logger.Debug("Runner socket (%s): %v", s.label, err)
				s.Terminate()
				return
			}
		}
	}
}

// ping pings every PingEvery, and drops the socket when nothing was heard for DeadAfter.
func (s *Session) ping() {
	t := time.NewTicker(s.pingEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, s.lastHeard.Load())) > s.deadAfter {
			logger.Warning("Runner socket (%s): nothing heard for %s, dropping it", s.label, s.deadAfter)
			s.Terminate()
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, s.deadAfter)
			defer cancel()
			if s.conn.Ping(ctx) == nil {
				s.lastHeard.Store(time.Now().UnixNano())
			}
		}()
	}
}
