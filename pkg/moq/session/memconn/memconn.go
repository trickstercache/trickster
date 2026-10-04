/*
 * Copyright 2026 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package memconn is an in-memory transport for MoQ sessions: two connected Conns whose streams keep FIN,
// reset and STOP_SENDING semantics, for tests that need sessions without sockets.
package memconn

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

const (
	acceptBacklog      = 1024
	datagramBacklog    = 256
	defaultMaxDatagram = 1200
)

var (
	// ErrClosedWrite is returned when writing to a stream after Close or CancelWrite
	ErrClosedWrite = errors.New("memconn: write on closed stream")
	// ErrDatagramTooLarge is returned when a datagram exceeds the connection's maximum
	ErrDatagramTooLarge = errors.New("memconn: datagram too large")
)

type pair struct {
	mu       sync.Mutex
	pipes    []*pipe
	closedBy *Conn
	closeErr *session.CloseError
}

// Conn is one end of an in-memory connection
type Conn struct {
	peer   *Conn
	pair   *pair
	bidi   chan *Stream
	uni    chan *ReceiveStream
	dgrams chan []byte
	ctx    context.Context
	cancel context.CancelCauseFunc
	// MaxDatagram bounds a datagram's size; datagrams beyond the receiver's backlog are dropped
	MaxDatagram int
	// Capacity, when positive, bounds the bytes a stream buffers before Write blocks
	Capacity int
}

var _ session.Conn = (*Conn)(nil)

// Pair returns two connected Conns
func Pair() (*Conn, *Conn) {
	shared := &pair{}
	mk := func() *Conn {
		ctx, cancel := context.WithCancelCause(context.Background())
		return &Conn{
			pair: shared, bidi: make(chan *Stream, acceptBacklog), uni: make(chan *ReceiveStream, acceptBacklog),
			dgrams: make(chan []byte, datagramBacklog), ctx: ctx, cancel: cancel, MaxDatagram: defaultMaxDatagram,
		}
	}
	a, b := mk(), mk()
	a.peer, b.peer = b, a
	return a, b
}

func (c *Conn) newPipe(writer *Conn) *pipe {
	p := &pipe{capacity: c.Capacity, writer: writer}
	p.cond = sync.NewCond(&p.mu)
	p.wctx, p.wcancel = context.WithCancelCause(context.Background())
	c.pair.mu.Lock()
	by, cerr := c.pair.closedBy, c.pair.closeErr
	if cerr == nil {
		c.pair.pipes = append(c.pair.pipes, p)
	}
	c.pair.mu.Unlock()
	if cerr != nil {
		p.close(by, cerr)
	}
	return p
}

// OpenStreamSync opens a bidirectional stream, which the peer accepts
func (c *Conn) OpenStreamSync(ctx context.Context) (session.Stream, error) {
	if c.ctx.Err() != nil {
		return nil, c.Cause()
	}
	out, in := c.newPipe(c), c.newPipe(c.peer)
	local := &Stream{SendStream: SendStream{p: out}, ReceiveStream: ReceiveStream{p: in}}
	remote := &Stream{SendStream: SendStream{p: in}, ReceiveStream: ReceiveStream{p: out}}
	select {
	case c.peer.bidi <- remote:
		return local, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.Cause()
	}
}

// OpenUniStreamSync opens a unidirectional stream, which the peer accepts
func (c *Conn) OpenUniStreamSync(ctx context.Context) (session.SendStream, error) {
	if c.ctx.Err() != nil {
		return nil, c.Cause()
	}
	p := c.newPipe(c)
	select {
	case c.peer.uni <- &ReceiveStream{p: p}:
		return &SendStream{p: p}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.Cause()
	}
}

// AcceptStream returns the next bidirectional stream the peer opened
func (c *Conn) AcceptStream(ctx context.Context) (session.Stream, error) {
	select {
	case s := <-c.bidi:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.Cause()
	}
}

// AcceptUniStream returns the next unidirectional stream the peer opened
func (c *Conn) AcceptUniStream(ctx context.Context) (session.ReceiveStream, error) {
	select {
	case s := <-c.uni:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.Cause()
	}
}

// SendDatagram delivers a copy of b to the peer, dropping it when the peer's backlog is full
func (c *Conn) SendDatagram(b []byte) error {
	if len(b) > c.MaxDatagram {
		return ErrDatagramTooLarge
	}
	if c.ctx.Err() != nil {
		return c.Cause()
	}
	select {
	case c.peer.dgrams <- append([]byte(nil), b...):
	default:
	}
	return nil
}

// ReceiveDatagram returns the next datagram the peer sent
func (c *Conn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.dgrams:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.Cause()
	}
}

// CloseWithError closes both ends with an application error
func (c *Conn) CloseWithError(code uint64, reason string) error {
	cerr := &session.CloseError{Code: code, Reason: reason}
	c.pair.mu.Lock()
	if c.pair.closeErr != nil {
		c.pair.mu.Unlock()
		return nil
	}
	c.pair.closedBy, c.pair.closeErr = c, cerr
	pipes := c.pair.pipes
	c.pair.pipes = nil
	c.pair.mu.Unlock()
	c.cancel(cerr)
	c.peer.cancel(&session.CloseError{Code: code, Reason: reason, Remote: true})
	for _, p := range pipes {
		p.close(c, cerr)
	}
	return nil
}

// Context is done when the connection closes
func (c *Conn) Context() context.Context {
	return c.ctx
}

// Cause returns the *session.CloseError the connection closed with
func (c *Conn) Cause() error {
	return context.Cause(c.ctx)
}

type pipe struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      []byte
	fin      bool
	reset    *uint64
	stopped  *uint64
	writer   *Conn
	closedBy *Conn
	closeErr *session.CloseError
	capacity int
	rdl, wdl time.Time
	wctx     context.Context
	wcancel  context.CancelCauseFunc
	urgency  int8
	incr     bool
	prioSet  bool
	boundary bool
}

func (p *pipe) close(by *Conn, err *session.CloseError) {
	p.mu.Lock()
	if p.closeErr == nil {
		p.closedBy, p.closeErr = by, err
	}
	p.cond.Broadcast()
	werr := p.closedFor(p.writer)
	p.mu.Unlock()
	p.wcancel(werr)
}

func (p *pipe) closedFor(c *Conn) error {
	// the caller holds p.mu; the connection's close error is reported as c's side of the connection sees it
	e := *p.closeErr
	e.Remote = c != p.closedBy
	return &e
}

func (p *pipe) wait(deadline time.Time) bool {
	// blocks until woken or the deadline passes, and reports false once it has passed
	if deadline.IsZero() {
		p.cond.Wait()
		return true
	}
	d := time.Until(deadline)
	if d <= 0 {
		return false
	}
	t := time.AfterFunc(d, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	p.cond.Wait()
	t.Stop()
	return true
}

// SendStream is the sending half of an in-memory stream
type SendStream struct {
	p *pipe
}

func (s *SendStream) Write(b []byte) (int, error) {
	p := s.p
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		switch {
		case p.stopped != nil:
			return 0, &session.StreamError{Code: *p.stopped, Remote: true}
		case p.reset != nil, p.fin:
			return 0, ErrClosedWrite
		case p.closeErr != nil:
			return 0, p.closedFor(p.writer)
		}
		if p.capacity <= 0 || len(p.buf) < p.capacity {
			break
		}
		if !p.wait(p.wdl) {
			return 0, os.ErrDeadlineExceeded
		}
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

// Close ends the stream with a FIN
func (s *SendStream) Close() error {
	p := s.p
	p.mu.Lock()
	if p.reset != nil || p.fin {
		p.mu.Unlock()
		return nil
	}
	p.fin = true
	p.cond.Broadcast()
	p.mu.Unlock()
	p.wcancel(nil)
	return nil
}

// CancelWrite resets the stream with code
func (s *SendStream) CancelWrite(code uint64) {
	p := s.p
	p.mu.Lock()
	if p.reset != nil {
		p.mu.Unlock()
		return
	}
	p.reset = &code
	p.cond.Broadcast()
	p.mu.Unlock()
	p.wcancel(&session.StreamError{Code: code})
}

// SetWriteDeadline bounds blocked writes
func (s *SendStream) SetWriteDeadline(t time.Time) error {
	s.p.mu.Lock()
	s.p.wdl = t
	s.p.cond.Broadcast()
	s.p.mu.Unlock()
	return nil
}

// SetPriority records the urgency and incremental flag, which the receiving half reports through Priority
func (s *SendStream) SetPriority(urgency int8, incremental bool) {
	s.p.mu.Lock()
	s.p.urgency, s.p.incr, s.p.prioSet = urgency, incremental, true
	s.p.mu.Unlock()
}

// SetReliableBoundary records that a reliable boundary was marked, which the receiving half reports
func (s *SendStream) SetReliableBoundary() {
	s.p.mu.Lock()
	s.p.boundary = true
	s.p.mu.Unlock()
}

// Context is done when the sending half closes, resets, or is stopped by the peer
func (s *SendStream) Context() context.Context {
	return s.p.wctx
}

// ReceiveStream is the receiving half of an in-memory stream
type ReceiveStream struct {
	p *pipe
}

func (r *ReceiveStream) Read(b []byte) (int, error) {
	p := r.p
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		switch {
		case p.stopped != nil:
			return 0, &session.StreamError{Code: *p.stopped}
		case len(p.buf) > 0:
			n := copy(b, p.buf)
			p.buf = p.buf[n:]
			p.cond.Broadcast()
			return n, nil
		case p.reset != nil:
			return 0, &session.StreamError{Code: *p.reset, Remote: true}
		case p.fin:
			return 0, io.EOF
		case p.closeErr != nil:
			return 0, p.closedFor(p.writer.peer)
		}
		if !p.wait(p.rdl) {
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// CancelRead stops the peer from sending with code
func (r *ReceiveStream) CancelRead(code uint64) {
	p := r.p
	p.mu.Lock()
	if p.stopped != nil {
		p.mu.Unlock()
		return
	}
	p.stopped = &code
	p.buf = nil
	p.cond.Broadcast()
	p.mu.Unlock()
	p.wcancel(&session.StreamError{Code: code, Remote: true})
}

// SetReadDeadline bounds blocked reads
func (r *ReceiveStream) SetReadDeadline(t time.Time) error {
	r.p.mu.Lock()
	r.p.rdl = t
	r.p.cond.Broadcast()
	r.p.mu.Unlock()
	return nil
}

// Priority returns the priority the sending half last set, and whether it set one
func (r *ReceiveStream) Priority() (urgency int8, incremental, set bool) {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	return r.p.urgency, r.p.incr, r.p.prioSet
}

// ReliableBoundary reports whether the sending half marked a reliable boundary
func (r *ReceiveStream) ReliableBoundary() bool {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	return r.p.boundary
}

// Stream is a bidirectional in-memory stream
type Stream struct {
	SendStream
	ReceiveStream
}
