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

package session

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// Role is the side of the transport connection an endpoint is on
type Role uint8

// Roles
const (
	RoleClient Role = iota
	RoleServer
)

const (
	defaultHandshakeTimeout = 10 * time.Second
	defaultMaxRequests      = 1024
	defaultAliasWait        = 2 * time.Second
	defaultMaxParked        = 64
	maxEarlyDatagrams       = 256
	tombstoneTTL            = 5 * time.Second
	maxTombstones           = 4096
	readerSize              = 4096
	maxOutOfOrderIDs        = 1024
	requiredRequestWait     = 2 * time.Second
	retryExcessiveLoad      = 1000
)

var (
	// ErrHandshakeTimeout is returned when the peer's SETUP does not arrive in time
	ErrHandshakeTimeout = errors.New("moq: no SETUP from the peer in time")
	// ErrSessionClosed is returned for work attempted after the session ended
	ErrSessionClosed = errors.New("moq: session closed")
	// ErrGoAwaySent is returned when sending a second GOAWAY on the control stream
	ErrGoAwaySent = errors.New("moq: GOAWAY already sent")
)

// Handler serves what the peer initiates on a session
type Handler interface {
	// HandleRequest serves one request the peer opened, on a goroutine of its own; it must call SetHandlers
	// to receive the request's later messages, and answer or cancel the request
	HandleRequest(r *Request)
	// HandleGoAway is told of a GOAWAY the peer sent on its control stream
	HandleGoAway(s *Session, m *message.GoAway)
}

// Options configure a session
type Options struct {
	Role  Role
	Codec *codec.Codec
	// WebTransport says the session runs over WebTransport, where SETUP carries no PATH or AUTHORITY
	WebTransport bool
	// Setup holds the options this endpoint sends in its SETUP
	Setup message.Setup
	// Handler serves the peer's requests and GOAWAY; nil refuses every request
	Handler Handler
	// OnSetup inspects the peer's SETUP before the session is ready; a *message.SessionError it returns closes
	// the session with that code
	OnSetup func(s *Session, peer *message.Setup) error
	// HandshakeTimeout bounds the wait for the peer's SETUP
	HandshakeTimeout time.Duration
	// MaxRequests bounds the requests the peer may have open at once
	MaxRequests int
	// UnknownAliasWait bounds how long a data stream naming an unknown track alias waits for that alias
	UnknownAliasWait time.Duration
	// MaxParked bounds the data streams waiting at once for an unknown track alias
	MaxParked int
}

// Session is one MoQ Transport session
type Session struct {
	conn  Conn
	codec *codec.Codec
	opts  Options

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    atomic.Pointer[error]

	ctrlMu     sync.Mutex
	ctrl       SendStream
	sentGoAway bool

	setupDone chan struct{}
	setupOnce sync.Once
	sentSetup atomic.Bool
	peerSetup atomic.Pointer[message.Setup]
	gotCtrl   atomic.Bool

	mu         sync.Mutex
	nextID     uint64
	peerIDs    idWindow
	handed     idWindow
	inbound    int
	aliases    map[uint64]*Request
	fetches    map[uint64]*Request
	waiting    map[uint64][]chan *Request
	requires   map[uint64][]chan struct{}
	parked     int
	early      map[uint64][]earlyDatagram
	earlyCount int
	tombstones map[uint64]time.Time
	nextAlias  uint64
	gotGoAway  bool
	live       map[*Request]struct{}
	tokens     tokenCache

	dgOnce  sync.Once
	dgQueue chan queuedDatagram

	endOnce sync.Once
}

// New returns a session over conn; Start runs it
func New(conn Conn, o Options) (*Session, error) {
	if conn == nil || o.Codec == nil {
		return nil, errors.New("moq: a session needs a connection and a codec")
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = defaultHandshakeTimeout
	}
	if o.MaxRequests <= 0 {
		o.MaxRequests = defaultMaxRequests
	}
	if o.UnknownAliasWait <= 0 {
		o.UnknownAliasWait = defaultAliasWait
	}
	if o.MaxParked <= 0 {
		o.MaxParked = defaultMaxParked
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		conn: conn, codec: o.Codec, opts: o, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		setupDone: make(chan struct{}), aliases: make(map[uint64]*Request), fetches: make(map[uint64]*Request),
		waiting: make(map[uint64][]chan *Request), early: make(map[uint64][]earlyDatagram),
		tombstones: make(map[uint64]time.Time),
		live:       make(map[*Request]struct{}), tokens: tokenCache{max: o.Setup.MaxAuthTokenCacheSize},
	}
	if o.Role == RoleServer {
		s.nextID = 1
	}
	return s, nil
}

// Start sends this endpoint's SETUP, starts serving the connection, and waits until the peer's SETUP arrives
func (s *Session) Start(ctx context.Context) error {
	go s.watch()
	go s.acceptUni()
	go s.acceptBidi()
	go s.receiveDatagrams()
	if err := s.sendSetup(ctx); err != nil {
		_ = s.Close(message.CodeInternalError, "unable to send SETUP")
		return err
	}
	t := time.NewTimer(s.opts.HandshakeTimeout)
	defer t.Stop()
	select {
	case <-s.setupDone:
		return nil
	case <-s.done:
		return s.Err()
	case <-ctx.Done():
		_ = s.Close(message.CodeInternalError, "handshake canceled")
		return ctx.Err()
	case <-t.C:
		_ = s.Close(message.CodeControlMessageTimeout, "no SETUP received")
		return ErrHandshakeTimeout
	}
}

// Codec returns the negotiated draft's codec
func (s *Session) Codec() *codec.Codec {
	return s.codec
}

// Role returns this endpoint's role
func (s *Session) Role() Role {
	return s.opts.Role
}

// WebTransport reports whether the session runs over WebTransport
func (s *Session) WebTransport() bool {
	return s.opts.WebTransport
}

// PeerSetup returns the peer's SETUP, or nil before it arrives
func (s *Session) PeerSetup() *message.Setup {
	return s.peerSetup.Load()
}

// Ready is closed once both SETUPs have been exchanged
func (s *Session) Ready() <-chan struct{} {
	return s.setupDone
}

// Done is closed when the session ends
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Context is canceled when the session ends
func (s *Session) Context() context.Context {
	return s.ctx
}

// Err returns why the session ended, or nil while it runs
func (s *Session) Err() error {
	if p := s.err.Load(); p != nil {
		return *p
	}
	return nil
}

// Close ends the session with a session termination code
func (s *Session) Close(code message.SessionCode, reason string) error {
	s.end(&CloseError{Code: uint64(code), Reason: reason})
	return s.conn.CloseWithError(uint64(code), reason)
}

func (s *Session) closedErr() error {
	// ErrSessionClosed joined with why the session ended, so callers can match either
	if err := s.Err(); err != nil && !errors.Is(err, ErrSessionClosed) {
		return fmt.Errorf("%w: %w", ErrSessionClosed, err)
	}
	return ErrSessionClosed
}

func (s *Session) fail(err error) {
	if se, ok := errors.AsType[*message.SessionError](err); ok {
		_ = s.Close(se.Code, se.Reason)
		return
	}
	_ = s.Close(message.CodeInternalError, err.Error())
}

func (s *Session) watch() {
	select {
	case <-s.conn.Context().Done():
		s.end(s.conn.Cause())
	case <-s.done:
	}
}

func (s *Session) end(err error) {
	s.endOnce.Do(func() {
		if err == nil {
			err = ErrSessionClosed
		}
		s.err.Store(&err)
		close(s.done)
		s.cancel()
		s.mu.Lock()
		reqs := make([]*Request, 0, len(s.live))
		for r := range s.live {
			reqs = append(reqs, r)
		}
		for _, chans := range s.waiting {
			for _, ch := range chans {
				close(ch)
			}
		}
		s.waiting = map[uint64][]chan *Request{}
		s.mu.Unlock()
		for _, r := range reqs {
			r.finish(err)
		}
	})
}

func (s *Session) sendSetup(ctx context.Context) error {
	st, err := s.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return err
	}
	st.SetPriority(UrgencyControl, false)
	setup := s.opts.Setup
	if s.opts.Role == RoleServer || s.opts.WebTransport {
		setup.HasPath, setup.HasAuthority = false, false
	}
	b, err := s.codec.AppendMessage(nil, &setup)
	if err != nil {
		return err
	}
	s.ctrlMu.Lock()
	s.ctrl = st
	_, err = st.Write(b)
	s.ctrlMu.Unlock()
	if err != nil {
		return err
	}
	s.sentSetup.Store(true)
	s.maybeReady()
	return nil
}

func (s *Session) maybeReady() {
	if s.sentSetup.Load() && s.peerSetup.Load() != nil {
		s.setupOnce.Do(func() { close(s.setupDone) })
	}
}

func (s *Session) waitReady() bool {
	select {
	case <-s.setupDone:
		return true
	case <-s.done:
		return false
	}
}

// GoAway asks the peer to migrate to a new session; a server may name the URI to migrate to, and timeout says
// how long this endpoint will wait before closing the session
func (s *Session) GoAway(uri string, timeout time.Duration) error {
	if s.opts.Role == RoleClient {
		uri = ""
	}
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	if s.sentGoAway {
		return ErrGoAwaySent
	}
	if s.ctrl == nil {
		return ErrSessionClosed
	}
	ms := uint64(max(timeout.Milliseconds(), 0)) // #nosec G115 -- clamped to zero or more
	b, err := s.codec.AppendMessage(nil, &message.GoAway{NewSessionURI: uri, Timeout: ms})
	if err != nil {
		return err
	}
	s.sentGoAway = true
	_, err = s.ctrl.Write(b)
	return err
}

var readers = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, readerSize) }}

func getReader(r io.Reader) *bufio.Reader {
	br := readers.Get().(*bufio.Reader)
	br.Reset(r)
	return br
}

func putReader(br *bufio.Reader) {
	br.Reset(nil)
	readers.Put(br)
}

func (s *Session) acceptUni() {
	for {
		rs, err := s.conn.AcceptUniStream(s.ctx)
		if err != nil {
			return
		}
		go s.handleUni(rs)
	}
}

func (s *Session) acceptBidi() {
	for {
		st, err := s.conn.AcceptStream(s.ctx)
		if err != nil {
			return
		}
		go s.handleBidi(st)
	}
}

func (s *Session) handleUni(rs ReceiveStream) {
	br := getReader(rs)
	defer putReader(br)
	t, err := wire.ReadVarintFrom(br)
	if err != nil {
		return
	}
	switch s.codec.ClassifyStream(t) {
	case codec.StreamControl:
		if !s.gotCtrl.CompareAndSwap(false, true) {
			s.fail(message.Violation("a second control stream"))
			return
		}
		s.serveControl(br)
	case codec.StreamSubgroup:
		s.serveSubgroup(t, rs, br)
	case codec.StreamFetch:
		s.serveFetch(rs, br)
	case codec.StreamPadding:
		rs.CancelRead(uint64(message.ResetCancelled))
	default:
		s.fail(message.Violation(fmt.Sprintf("unknown stream type %#x", t)))
	}
}

func (s *Session) serveControl(br *bufio.Reader) {
	// the stream type was SETUP's message type, so the SETUP's length and body come next
	var lb [2]byte
	if _, err := io.ReadFull(br, lb[:]); err != nil {
		s.controlEnded(err, "control stream ended before SETUP")
		return
	}
	body := make([]byte, binary.BigEndian.Uint16(lb[:]))
	if _, err := io.ReadFull(br, body); err != nil {
		s.controlEnded(err, "control stream ended within SETUP")
		return
	}
	m, err := s.codec.ParseMessage(codec.StreamTypeControl, body)
	if err != nil {
		s.fail(err)
		return
	}
	setup := m.(*message.Setup)
	if err = s.checkPeerSetup(setup); err != nil {
		s.fail(err)
		return
	}
	s.peerSetup.Store(setup)
	s.maybeReady()
	for {
		typ, body, err := wire.ReadMessage(br, nil)
		if err != nil {
			s.controlEnded(err, "control stream closed")
			return
		}
		k, ok := s.codec.MessageKind(typ)
		if !ok || k != message.KindGoAway {
			s.fail(message.Violation("unexpected message on the control stream"))
			return
		}
		m, err := s.codec.ParseMessage(typ, body)
		if err != nil {
			s.fail(err)
			return
		}
		if err = s.receiveGoAway(m.(*message.GoAway)); err != nil {
			s.fail(err)
			return
		}
	}
}

func (s *Session) controlEnded(err error, what string) {
	// the connection closing ends the session with its cause, while the peer ending the control stream is a violation
	if ce, ok := errors.AsType[*CloseError](err); ok {
		s.end(ce)
		return
	}
	if s.conn.Context().Err() != nil {
		s.end(s.conn.Cause())
		return
	}
	if s.ctx.Err() == nil {
		s.fail(message.Violation(what))
	}
}

func (s *Session) checkPeerSetup(m *message.Setup) error {
	switch {
	case m.HasAuthority && (s.opts.Role == RoleClient || s.opts.WebTransport):
		return &message.SessionError{Code: message.CodeInvalidAuthority, Reason: "AUTHORITY not allowed here"}
	case m.HasPath && (s.opts.Role == RoleClient || s.opts.WebTransport):
		return &message.SessionError{Code: message.CodeInvalidPath, Reason: "PATH not allowed here"}
	}
	for i := range m.AuthTokens {
		tok := m.AuthTokens[i]
		if s.opts.Role == RoleServer && (tok.AliasType == wire.TokenDelete || tok.AliasType == wire.TokenUseAlias) {
			return message.Violation("SETUP may only register or use token values")
		}
		resolved, err := s.tokens.apply(tok, true)
		if err != nil {
			return err
		}
		m.AuthTokens[i] = resolved
	}
	if s.opts.OnSetup != nil {
		return s.opts.OnSetup(s, m)
	}
	return nil
}

func (s *Session) receiveGoAway(m *message.GoAway) error {
	s.mu.Lock()
	dup := s.gotGoAway
	s.gotGoAway = true
	s.mu.Unlock()
	if dup {
		return message.Violation("a second GOAWAY on the control stream")
	}
	if s.opts.Role == RoleServer && m.NewSessionURI != "" {
		return message.Violation("a client's GOAWAY may not name a URI")
	}
	if s.opts.Handler != nil {
		s.opts.Handler.HandleGoAway(s, m)
	}
	return nil
}

// GoAwayReceived reports whether the peer sent GOAWAY on its control stream
func (s *Session) GoAwayReceived() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotGoAway
}

func (s *Session) allocRequestID() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID += 2
	return id
}

// NewTrackAlias returns a track alias not yet used by this endpoint in the session
func (s *Session) NewTrackAlias() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.nextAlias
	s.nextAlias++
	return a
}

func (s *Session) acceptPeerID(id uint64) error {
	// the client's request IDs are even and the server's odd; each may arrive out of order but only once
	wantOdd := s.opts.Role == RoleClient
	if (id%2 == 1) != wantOdd {
		return &message.SessionError{Code: message.CodeInvalidRequestID, Reason: "request ID of the wrong parity"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.peerIDs.add(id / 2) {
		return &message.SessionError{Code: message.CodeInvalidRequestID, Reason: "duplicate request ID"}
	}
	return nil
}

func (s *Session) handedOff(id uint64) {
	// a request given to the handler, or refused, releases those that named it as the one they require (§9.2)
	if !s.codec.Has(codec.CapRequiredRequestID) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handed.add(id / 2)
	for _, ch := range s.requires[id/2] {
		close(ch)
	}
	delete(s.requires, id/2)
}

func (s *Session) awaitRequired(id, delta uint64) (bool, error) {
	// draft -17: a request names one it depends on, which must be delivered first or the dependent one times out
	if delta == 0 || !s.codec.Has(codec.CapRequiredRequestID) {
		return true, nil
	}
	if delta > id/2 {
		return false, &message.SessionError{
			Code:   message.CodeInvalidRequiredRequestID,
			Reason: "required request ID below zero",
		}
	}
	need := (id - 2*delta) / 2
	s.mu.Lock()
	if s.handed.has(need) {
		s.mu.Unlock()
		return true, nil
	}
	ch := make(chan struct{})
	if s.requires == nil {
		s.requires = make(map[uint64][]chan struct{})
	}
	s.requires[need] = append(s.requires[need], ch)
	s.mu.Unlock()
	t := time.NewTimer(requiredRequestWait)
	defer t.Stop()
	select {
	case <-ch:
		return true, nil
	case <-t.C:
	case <-s.ctx.Done():
	}
	s.mu.Lock()
	waiters := s.requires[need]
	if i := slices.Index(waiters, ch); i >= 0 {
		s.requires[need] = slices.Delete(waiters, i, i+1)
		if len(s.requires[need]) == 0 {
			delete(s.requires, need)
		}
	}
	s.mu.Unlock()
	return false, nil
}

type idWindow struct {
	low  uint64
	seen map[uint64]struct{}
}

func (w *idWindow) add(n uint64) bool {
	if n < w.low {
		return false
	}
	if _, dup := w.seen[n]; dup {
		return false
	}
	if n == w.low {
		w.low++
		w.advance()
		return true
	}
	if w.seen == nil {
		w.seen = make(map[uint64]struct{})
	}
	w.seen[n] = struct{}{}
	if len(w.seen) > maxOutOfOrderIDs {
		// a peer that skipped IDs must not grow the window without bound, so the gap below the least ID
		// waiting is treated as used
		least, first := w.low, true
		for k := range w.seen {
			if first || k < least {
				least, first = k, false
			}
		}
		w.low = least
		w.advance()
	}
	return true
}

func (w *idWindow) has(n uint64) bool {
	// an ID below the window was accepted, or given up on when the window had to move past it
	if n < w.low {
		return true
	}
	_, ok := w.seen[n]
	return ok
}

func (w *idWindow) advance() {
	for {
		if _, ok := w.seen[w.low]; !ok {
			return
		}
		delete(w.seen, w.low)
		w.low++
	}
}

func (s *Session) handleBidi(st Stream) {
	if !s.waitReady() {
		st.CancelRead(uint64(message.ResetSessionClosed))
		st.CancelWrite(uint64(message.ResetSessionClosed))
		return
	}
	br := getReader(st)
	typ, body, err := wire.ReadMessage(br, nil)
	if err != nil {
		putReader(br)
		st.CancelRead(uint64(message.ResetCancelled))
		st.CancelWrite(uint64(message.ResetCancelled))
		return
	}
	k, ok := s.codec.MessageKind(typ)
	if !ok || !k.IsRequest() {
		putReader(br)
		s.fail(message.Violation("a request stream must begin with a request"))
		return
	}
	m, err := s.codec.ParseMessage(typ, body)
	if err != nil {
		putReader(br)
		s.fail(err)
		return
	}
	id, _ := message.RequestIDOf(m)
	if err = s.acceptPeerID(id); err != nil {
		putReader(br)
		s.fail(err)
		return
	}
	arrived, err := s.awaitRequired(id, message.RequiredRequestIDOf(m))
	if err != nil {
		putReader(br)
		s.fail(err)
		return
	}
	r := newRequest(s, st, br, false, k, m, id)
	if err = r.resolveTokens(); err != nil {
		r.release()
		s.fail(err)
		return
	}
	if !arrived {
		_ = r.Reject(message.RequestTimeout, "the required request did not arrive", 0)
		s.handedOff(id)
		r.readLoop()
		return
	}
	if reason, reserved := reservedNamespace(m); reserved {
		_ = r.Reject(message.RequestDoesNotExist, reason, 0)
		s.handedOff(id)
		r.readLoop()
		return
	}
	s.mu.Lock()
	full := s.inbound >= s.opts.MaxRequests
	if !full {
		s.inbound++
		r.counted = true
	}
	s.mu.Unlock()
	if full {
		_ = r.Reject(message.RequestExcessiveLoad, "too many requests", retryExcessiveLoad)
		s.handedOff(id)
		r.readLoop()
		return
	}
	if p, isPublish := m.(*message.Publish); isPublish {
		if err = s.registerAlias(p.TrackAlias, r); err != nil {
			r.release()
			s.fail(err)
			return
		}
		r.alias, r.hasAlias = p.TrackAlias, true
	}
	if !s.track(r) {
		r.release()
		return
	}
	if s.opts.Handler == nil {
		_ = r.Reject(message.RequestNotSupported, "requests are not served", 0)
	} else {
		go s.opts.Handler.HandleRequest(r)
	}
	s.handedOff(id)
	r.readLoop()
}

func (s *Session) track(r *Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.live[r] = struct{}{}
	return true
}

func (s *Session) untrack(r *Request, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, r)
	if r.counted {
		s.inbound--
		r.counted = false
	}
	if r.hasAlias && s.aliases[r.alias] == r {
		delete(s.aliases, r.alias)
		s.tombstone(r.alias)
	}
	r.mu.Lock()
	routed, refused := r.fetchOrders != nil, r.refused
	r.mu.Unlock()
	if !routed {
		return
	}
	// a fetch or fill stream can trail the request stream that asked for it, so a request that ended cleanly
	// keeps its routes briefly; a stream that never comes must not hold them for the session's life
	if err == nil && !refused {
		time.AfterFunc(tombstoneTTL, func() { s.dropFetchRoutes(r) })
		return
	}
	s.dropFetchRoutesLocked(r)
}

func (s *Session) dropFetchRoutes(r *Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropFetchRoutesLocked(r)
}

func (s *Session) dropFetchRoutesLocked(r *Request) {
	for id, fr := range s.fetches {
		if fr == r {
			delete(s.fetches, id)
		}
	}
}

func reservedNamespace(m message.Message) (string, bool) {
	// no session-level tracks are defined, the reserved "." namespace is never used, and a relay forwards
	// neither, so such a request is refused here rather than reaching the application
	var ns wire.Namespace
	switch v := m.(type) {
	case *message.Subscribe:
		ns = v.Track.Namespace
	case *message.Fetch:
		ns = v.Track.Namespace
	case *message.TrackStatus:
		ns = v.Track.Namespace
	case *message.Publish:
		ns = v.Track.Namespace
	case *message.PublishNamespace:
		ns = v.Prefix
	case *message.SubscribeNamespace:
		ns = v.Prefix
	case *message.SubscribeTracks:
		ns = v.Prefix
	}
	if len(ns) == 0 {
		return "", false
	}
	switch string(ns[0]) {
	case sessionNamespace:
		return "no session-level tracks are defined", true
	case reservedDot:
		return "the namespace is reserved", true
	}
	return "", false
}

const (
	sessionNamespace = ".session"
	reservedDot      = "."
)
