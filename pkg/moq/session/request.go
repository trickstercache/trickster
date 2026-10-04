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
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

var (
	// ErrNotAllowed is returned when sending a message the request's state does not permit
	ErrNotAllowed = errors.New("moq: message not allowed in the request's state")
	// ErrRequestEnded is returned for work on a request that has ended
	ErrRequestEnded = errors.New("moq: request ended")
	// ErrNoResponse is returned when a request's stream ends before its response
	ErrNoResponse = errors.New("moq: request stream ended before a response")
	// ErrNoPublishDone ends a subscription whose stream the publisher closed without PUBLISH_DONE
	ErrNoPublishDone = errors.New("moq: subscription ended without PUBLISH_DONE")
	// ErrTooManyUpdates is returned when the peer's MAX_REQUEST_UPDATES would be exceeded
	ErrTooManyUpdates = errors.New("moq: too many outstanding REQUEST_UPDATEs")
	// ErrNotRequest is returned when opening a request with a message that is not one
	ErrNotRequest = errors.New("moq: message does not open a request")
	// ErrNoTrackAlias is returned when sending objects on a request that has no track alias
	ErrNoTrackAlias = errors.New("moq: request has no track alias")
)

// RequestHandler receives a request's later messages and its end
type RequestHandler interface {
	// HandleMessage is called with each message after the request, and after the response for a local
	// request, in order and already checked against the request's state; it must not block for long
	HandleMessage(r *Request, m message.Message)
	// HandleClose is called once when the request ends, with nil when both directions closed cleanly
	HandleClose(r *Request, err error)
}

// DataHandler receives the objects of a request this endpoint subscribes or fetches through
type DataHandler interface {
	// HandleSubgroup serves one incoming subgroup stream on a goroutine of its own
	HandleSubgroup(r *Request, in *IncomingSubgroup)
	// HandleDatagram receives one object datagram; it must not block, and d and its payload are valid only
	// during the call
	HandleDatagram(r *Request, d *message.Datagram)
	// HandleFetch serves one incoming fetch stream on a goroutine of its own
	HandleFetch(r *Request, in *IncomingFetch)
}

type kindSet uint32

func kinds(ks ...message.Kind) kindSet {
	var s kindSet
	for _, k := range ks {
		s |= 1 << k
	}
	return s
}

func (s kindSet) has(k message.Kind) bool {
	return s&(1<<k) != 0
}

type flow struct {
	first     kindSet
	responder kindSet
	requester kindSet
}

var (
	updateAnswers = kinds(message.KindRequestOK, message.KindRequestError)
	flows         = map[message.Kind]flow{
		message.KindSubscribe: {
			first: kinds(message.KindSubscribeOK, message.KindRequestError),
			responder: updateAnswers | kinds(message.KindPublishStateNotify, message.KindPublishDone,
				message.KindGoAway),
			requester: kinds(message.KindRequestUpdate, message.KindGoAway),
		},
		message.KindPublish: {
			first:     kinds(message.KindRequestOK, message.KindRequestError, message.KindPublishOK),
			responder: kinds(message.KindRequestUpdate, message.KindGoAway),
			requester: updateAnswers | kinds(message.KindPublishStateNotify, message.KindPublishDone,
				message.KindGoAway),
		},
		message.KindFetch: {
			first:     kinds(message.KindFetchOK, message.KindRequestError),
			responder: updateAnswers | kinds(message.KindGoAway),
			requester: kinds(message.KindRequestUpdate, message.KindGoAway),
		},
		message.KindTrackStatus: {
			first: updateAnswers,
		},
		message.KindPublishNamespace: {
			first:     updateAnswers,
			responder: updateAnswers | kinds(message.KindGoAway),
			requester: kinds(message.KindRequestUpdate, message.KindGoAway),
		},
		message.KindSubscribeNamespace: {
			first:     updateAnswers,
			responder: updateAnswers | kinds(message.KindNamespace, message.KindNamespaceDone, message.KindGoAway),
			requester: kinds(message.KindRequestUpdate, message.KindGoAway),
		},
		message.KindSubscribeTracks: {
			first:     updateAnswers,
			responder: updateAnswers | kinds(message.KindPublishSkipped, message.KindGoAway),
			requester: kinds(message.KindRequestUpdate, message.KindGoAway),
		},
	}
)

// Request is one request stream, opened by either endpoint
type Request struct {
	s     *Session
	st    Stream
	br    *bufio.Reader
	local bool
	kind  message.Kind
	msg   message.Message
	id    uint64

	wmu  sync.Mutex
	wbuf []byte

	mu          sync.Mutex
	responded   bool
	refused     bool
	termSent    bool
	termRecv    bool
	sendClosed  bool
	recvClosed  bool
	updatesIn   uint64
	updatesOut  uint64
	goAwayIn    bool
	goAwayOut   bool
	suffixes    map[string]struct{}
	handler     RequestHandler
	data        DataHandler
	ready       chan struct{}
	readyOnce   sync.Once
	resp        chan message.Message
	counted     bool
	alias       uint64
	hasAlias    bool
	fetchOrders map[uint64]message.GroupOrder
	streams     atomic.Uint64
	brOnce      sync.Once

	ctx     context.Context
	cancel  context.CancelFunc
	endOnce sync.Once
	err     error
}

func newRequest(s *Session, st Stream, br *bufio.Reader, local bool, k message.Kind, m message.Message,
	id uint64,
) *Request {
	r := &Request{
		s: s, st: st, br: br, local: local, kind: k, msg: m, id: id, ready: make(chan struct{}),
		resp: make(chan message.Message, 1),
	}
	r.ctx, r.cancel = context.WithCancel(s.ctx)
	context.AfterFunc(st.Context(), func() {
		// the peer stopping our sending half cancels the request
		if se, ok := errors.AsType[*StreamError](streamErr(context.Cause(st.Context()))); ok && se.Remote {
			r.finish(se)
		}
	})
	return r
}

// Kind returns the kind of the message that opened the request
func (r *Request) Kind() message.Kind {
	return r.kind
}

// Message returns the message that opened the request
func (r *Request) Message() message.Message {
	return r.msg
}

// RequestID returns the request's ID
func (r *Request) RequestID() uint64 {
	return r.id
}

// Local reports whether this endpoint opened the request
func (r *Request) Local() bool {
	return r.local
}

// Session returns the session the request belongs to
func (r *Request) Session() *Session {
	return r.s
}

// Context is canceled when the request ends
func (r *Request) Context() context.Context {
	return r.ctx
}

// Err returns why the request ended, once it has
func (r *Request) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// TrackAlias returns the alias objects of this request travel under, once one is known
func (r *Request) TrackAlias() (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alias, r.hasAlias
}

// StreamCount returns how many data streams this endpoint opened for the request, for PUBLISH_DONE
func (r *Request) StreamCount() uint64 {
	return r.streams.Load()
}

// SetHandlers sets what receives the request's later messages and incoming objects; a request the peer opened
// holds its later messages and objects until this is called
func (r *Request) SetHandlers(h RequestHandler, d DataHandler) {
	r.mu.Lock()
	r.handler, r.data = h, d
	r.mu.Unlock()
	r.readyOnce.Do(func() { close(r.ready) })
}

// Response waits for the first response to a request this endpoint opened
func (r *Request) Response(ctx context.Context) (message.Message, error) {
	select {
	case m := <-r.resp:
		r.resp <- m
		return m, nil
	case <-r.ctx.Done():
		select {
		case m := <-r.resp:
			r.resp <- m
			return m, nil
		default:
		}
		if err := r.Err(); err != nil {
			return nil, err
		}
		return nil, ErrRequestEnded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Send sends a message on the request stream after checking it against the request's state
func (r *Request) Send(m message.Message) error {
	if m.Kind() == message.KindRequestUpdate {
		return fmt.Errorf("%w: use Update", ErrNotAllowed)
	}
	return r.send(m)
}

func (r *Request) send(m message.Message) error {
	r.wmu.Lock()
	defer r.wmu.Unlock()
	if r.ctx.Err() != nil {
		return ErrRequestEnded
	}
	r.mu.Lock()
	err := r.checkSend(m)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	b, err := r.s.codec.AppendMessage(r.wbuf[:0], m)
	if err != nil {
		return err
	}
	r.wbuf = b
	_, err = r.st.Write(b)
	return err
}

// Reject answers a request with REQUEST_ERROR and closes this endpoint's half of the stream
func (r *Request) Reject(code message.RequestCode, reason string, retryInterval uint64) error {
	if err := r.Send(&message.RequestError{Code: code, Reason: reason, RetryInterval: retryInterval}); err != nil {
		return err
	}
	return r.CloseSend()
}

// Update sends a REQUEST_UPDATE on the request, returning the Request ID it consumed
func (r *Request) Update(p message.Params) (uint64, error) {
	if limit := r.s.peerMaxUpdates(); limit > 0 {
		r.mu.Lock()
		over := r.updatesOut >= limit
		r.mu.Unlock()
		if over {
			return 0, ErrTooManyUpdates
		}
	}
	id := r.s.allocRequestID()
	if p.Has(message.ParamFillParameters) {
		r.s.routeFetch(id, r, r.fillOrder(p.Fill))
	}
	if err := r.send(&message.RequestUpdate{RequestID: id, Params: p}); err != nil {
		return id, err
	}
	return id, nil
}

// CloseSend closes this endpoint's half of the request stream with a FIN
func (r *Request) CloseSend() error {
	r.wmu.Lock()
	r.mu.Lock()
	if r.sendClosed {
		r.mu.Unlock()
		r.wmu.Unlock()
		return nil
	}
	r.sendClosed = true
	both := r.recvClosed
	r.mu.Unlock()
	err := r.st.Close()
	r.wmu.Unlock()
	if both {
		r.finish(nil)
	}
	return err
}

// Cancel abandons the request, resetting both halves of its stream with code
func (r *Request) Cancel(code message.ResetCode) {
	r.st.CancelWrite(uint64(code))
	r.st.CancelRead(uint64(code))
	r.finish(&StreamError{Code: uint64(code)})
}

func (r *Request) checkSend(m message.Message) error {
	f := flows[r.kind]
	k := m.Kind()
	if r.sendClosed || r.termSent {
		return fmt.Errorf("%w: %s after this side finished", ErrNotAllowed, k)
	}
	requester := r.local
	if !requester && !r.responded {
		if !f.first.has(k) {
			return fmt.Errorf("%w: %s cannot answer %s", ErrNotAllowed, k, r.kind)
		}
		r.responded = true
		r.termSent = k == message.KindRequestError || r.kind == message.KindTrackStatus
		if ok, isOK := m.(*message.SubscribeOK); isOK {
			r.alias, r.hasAlias = ok.TrackAlias, true
		}
		return nil
	}
	allowed := f.responder
	if requester {
		allowed = f.requester
	}
	if !allowed.has(k) {
		return fmt.Errorf("%w: %s on %s", ErrNotAllowed, k, r.kind)
	}
	switch k {
	case message.KindRequestOK, message.KindRequestError:
		if r.updatesIn == 0 {
			return fmt.Errorf("%w: no REQUEST_UPDATE to answer", ErrNotAllowed)
		}
		if k == message.KindRequestError {
			r.updatesIn = 0
		} else {
			r.updatesIn--
		}
	case message.KindRequestUpdate:
		r.updatesOut++
	case message.KindPublishDone:
		r.termSent = true
	case message.KindGoAway:
		if r.goAwayOut {
			return fmt.Errorf("%w: a second GOAWAY", ErrNotAllowed)
		}
		r.goAwayOut = true
	}
	return nil
}

func (r *Request) release() {
	r.brOnce.Do(func() {
		if r.br != nil {
			putReader(r.br)
		}
	})
}

func (r *Request) readLoop() {
	defer r.release()
	for {
		typ, body, err := wire.ReadMessage(r.br, nil)
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.receivedFIN()
			} else {
				r.finish(streamErr(err))
			}
			return
		}
		if _, ok := r.s.codec.MessageKind(typ); !ok {
			r.s.fail(message.Violation(fmt.Sprintf("unknown message type %#x on a request stream", typ)))
			return
		}
		m, err := r.s.codec.ParseMessage(typ, body)
		if err != nil {
			r.s.fail(err)
			return
		}
		if err = r.receive(m); err != nil {
			r.s.fail(err)
			return
		}
	}
}

func (r *Request) receive(m message.Message) error {
	first, err := r.checkReceive(m)
	if err != nil {
		return err
	}
	switch v := m.(type) {
	case *message.SubscribeOK:
		if err = r.s.registerAlias(v.TrackAlias, r); err != nil {
			return err
		}
		r.mu.Lock()
		r.alias, r.hasAlias = v.TrackAlias, true
		r.mu.Unlock()
		if order := v.TrackProperties.GroupOrder(); order != message.GroupOrderAscending {
			r.adoptTrackOrder(order)
		}
	case *message.RequestUpdate:
		if err = r.s.acceptPeerID(v.RequestID); err != nil {
			return err
		}
		if err = r.s.resolveTokens(&v.Params); err != nil {
			return err
		}
	}
	if first {
		r.resp <- m
		return nil
	}
	select {
	case <-r.ready:
	case <-r.ctx.Done():
		return nil
	}
	r.mu.Lock()
	h := r.handler
	r.mu.Unlock()
	if h != nil {
		h.HandleMessage(r, m)
	}
	return nil
}

func (r *Request) checkReceive(m message.Message) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := flows[r.kind]
	k := m.Kind()
	if r.termRecv {
		return false, message.Violation(fmt.Sprintf("%s after the peer finished %s", k, r.kind))
	}
	fromResponder := r.local
	if fromResponder && !r.responded {
		if !f.first.has(k) {
			return false, message.Violation(fmt.Sprintf("%s cannot answer %s", k, r.kind))
		}
		r.responded = true
		r.refused = k == message.KindRequestError
		r.termRecv = r.refused || r.kind == message.KindTrackStatus
		return true, nil
	}
	allowed := f.requester
	if fromResponder {
		allowed = f.responder
	}
	if !allowed.has(k) {
		return false, message.Violation(fmt.Sprintf("%s not allowed on %s", k, r.kind))
	}
	switch k {
	case message.KindRequestOK, message.KindRequestError:
		if r.updatesOut == 0 {
			return false, message.Violation("an answer with no REQUEST_UPDATE outstanding")
		}
		if k == message.KindRequestError {
			r.updatesOut = 0
		} else {
			r.updatesOut--
		}
	case message.KindRequestUpdate:
		r.updatesIn++
		if limit := r.s.opts.Setup.MaxRequestUpdates; limit > 0 && r.updatesIn > limit {
			return false, &message.SessionError{Code: message.CodeTooManyRequestUpdates, Reason: "too many updates"}
		}
	case message.KindPublishDone:
		r.termRecv = true
	case message.KindGoAway:
		if r.goAwayIn {
			return false, message.Violation("a second GOAWAY on a request stream")
		}
		r.goAwayIn = true
	case message.KindNamespace, message.KindNamespaceDone:
		return false, r.trackSuffix(m)
	}
	return false, nil
}

func (r *Request) trackSuffix(m message.Message) error {
	// NAMESPACE_DONE must follow the NAMESPACE it withdraws
	var suffix wire.Namespace
	done := false
	switch v := m.(type) {
	case *message.Namespace:
		suffix = v.Suffix
	case *message.NamespaceDone:
		suffix, done = v.Suffix, true
	}
	key := string(wire.AppendNamespace(nil, suffix))
	if r.suffixes == nil {
		r.suffixes = make(map[string]struct{})
	}
	if !done {
		r.suffixes[key] = struct{}{}
		return nil
	}
	if _, ok := r.suffixes[key]; !ok {
		return message.Violation("NAMESPACE_DONE before its NAMESPACE")
	}
	delete(r.suffixes, key)
	return nil
}

func (r *Request) receivedFIN() {
	r.mu.Lock()
	r.recvClosed = true
	var failed error
	switch {
	case r.local && !r.responded:
		failed = ErrNoResponse
	case r.local && !r.termRecv && r.kind == message.KindSubscribe:
		failed = ErrNoPublishDone
	}
	sendOpen := !r.sendClosed
	r.mu.Unlock()
	if failed != nil {
		r.finish(failed)
		_ = r.CloseSend()
		return
	}
	if r.local && sendOpen {
		// the responder finished, so the requester closes its half too
		_ = r.CloseSend()
		return
	}
	r.mu.Lock()
	both := r.sendClosed
	r.mu.Unlock()
	if both {
		r.finish(nil)
	}
}

func (r *Request) finish(err error) {
	r.endOnce.Do(func() {
		r.mu.Lock()
		r.err = err
		h := r.handler
		r.mu.Unlock()
		r.cancel()
		r.s.untrack(r, err)
		if h != nil {
			h.HandleClose(r, err)
		}
	})
}

func (r *Request) resolveTokens() error {
	switch v := r.msg.(type) {
	case *message.Subscribe:
		return r.s.resolveTokens(&v.Params)
	case *message.Publish:
		return r.s.resolveTokens(&v.Params)
	case *message.Fetch:
		return r.s.resolveTokens(&v.Params)
	case *message.TrackStatus:
		return r.s.resolveTokens(&v.Params)
	case *message.PublishNamespace:
		return r.s.resolveTokens(&v.Params)
	case *message.SubscribeNamespace:
		return r.s.resolveTokens(&v.Params)
	case *message.SubscribeTracks:
		return r.s.resolveTokens(&v.Params)
	}
	return nil
}

func (s *Session) resolveTokens(p *message.Params) error {
	if len(p.AuthTokens) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := p.AuthTokens[:0]
	for _, t := range p.AuthTokens {
		resolved, err := s.tokens.apply(t, false)
		if err != nil {
			return err
		}
		if t.AliasType != wire.TokenDelete {
			out = append(out, resolved)
		}
	}
	p.AuthTokens = out
	return nil
}

func (s *Session) peerMaxUpdates() uint64 {
	if p := s.peerSetup.Load(); p != nil {
		return p.MaxRequestUpdates
	}
	return 0
}

// Open sends a request on a new stream; h receives its later messages, and d any objects it delivers to this
// endpoint. The message's Request ID is assigned here.
func (s *Session) Open(ctx context.Context, m message.Message, h RequestHandler, d DataHandler) (*Request, error) {
	k := m.Kind()
	if !k.IsRequest() {
		return nil, ErrNotRequest
	}
	if s.ctx.Err() != nil {
		return nil, s.closedErr()
	}
	select {
	case <-s.setupDone:
	case <-s.done:
		return nil, s.closedErr()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	id := s.allocRequestID()
	setRequestID(m, id)
	st, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, s.closedErr()
		}
		return nil, err
	}
	st.SetPriority(UrgencyControl, false)
	r := newRequest(s, st, getReader(st), true, k, m, id)
	r.SetHandlers(h, d)
	switch v := m.(type) {
	case *message.Fetch:
		order := v.Params.GroupOrder
		if !v.Params.Has(message.ParamGroupOrder) {
			order = message.GroupOrderAscending
		}
		s.routeFetch(id, r, order)
	case *message.Subscribe:
		if v.Params.Has(message.ParamFillParameters) {
			s.routeFetch(id, r, r.fillOrder(v.Params.Fill))
		}
	case *message.Publish:
		r.alias, r.hasAlias = v.TrackAlias, true
	}
	if !s.track(r) {
		st.CancelWrite(uint64(message.ResetSessionClosed))
		r.release()
		return nil, s.closedErr()
	}
	b, err := s.codec.AppendMessage(nil, m)
	if err == nil {
		_, err = st.Write(b)
	}
	if err != nil {
		r.Cancel(message.ResetInternalError)
		return nil, err
	}
	go r.readLoop()
	return r, nil
}

func setRequestID(m message.Message, id uint64) {
	switch v := m.(type) {
	case *message.Subscribe:
		v.RequestID = id
	case *message.Publish:
		v.RequestID = id
	case *message.Fetch:
		v.RequestID = id
	case *message.TrackStatus:
		v.RequestID = id
	case *message.PublishNamespace:
		v.RequestID = id
	case *message.SubscribeNamespace:
		v.RequestID = id
	case *message.SubscribeTracks:
		v.RequestID = id
	}
}

func (r *Request) fillOrder(fill *message.Params) message.GroupOrder {
	// a fill fetch stream uses its own GROUP_ORDER, else the subscription's, else ascending
	if fill != nil && fill.Has(message.ParamGroupOrder) {
		return fill.GroupOrder
	}
	if sub, ok := r.msg.(*message.Subscribe); ok && sub.Params.Has(message.ParamGroupOrder) {
		return sub.Params.GroupOrder
	}
	return message.GroupOrderAscending
}

func (r *Request) adoptTrackOrder(order message.GroupOrder) {
	// a subscription that named no group order follows the track's default, learned from SUBSCRIBE_OK
	sub, ok := r.msg.(*message.Subscribe)
	if !ok || sub.Params.Has(message.ParamGroupOrder) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.fetchOrders {
		if sub.Params.Fill == nil || !sub.Params.Fill.Has(message.ParamGroupOrder) {
			r.fetchOrders[id] = order
		}
	}
}

func (r *Request) fetchOrder(id uint64) message.GroupOrder {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.fetchOrders[id]; ok {
		return o
	}
	return message.GroupOrderAscending
}
