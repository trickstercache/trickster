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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
)

var (
	// ErrPayloadPending is returned when an object's header is written before the last object's payload is done
	ErrPayloadPending = errors.New("moq: the previous object's payload is incomplete")
	// ErrPayloadOverrun is returned when writing more payload than the object's declared length
	ErrPayloadOverrun = errors.New("moq: payload exceeds the object's length")
	// ErrDatagramDropped is returned when a datagram finds the session's send queue full
	ErrDatagramDropped = errors.New("moq: datagram dropped with the send queue full")
)

func (s *Session) registerAlias(alias uint64, r *Request) error {
	s.mu.Lock()
	if cur, ok := s.aliases[alias]; ok && cur != r && cur.ctx.Err() == nil {
		same := trackOf(cur.msg).Equal(trackOf(r.msg))
		s.mu.Unlock()
		if !same {
			return &message.SessionError{Code: message.CodeDuplicateTrackAlias, Reason: "track alias names another track"}
		}
		// a second subscription to the same track may share its alias; its objects reach the first
		return nil
	}
	s.aliases[alias] = r
	delete(s.tombstones, alias)
	waiters := s.waiting[alias]
	delete(s.waiting, alias)
	s.parked -= len(waiters)
	early := s.early[alias]
	if early != nil {
		delete(s.early, alias)
		s.earlyCount -= len(early)
	}
	s.mu.Unlock()
	for _, ch := range waiters {
		ch <- r
	}
	if early != nil {
		go s.deliverEarly(r, early, time.Now())
	}
	return nil
}

type earlyDatagram struct {
	b       []byte
	arrived time.Time
}

func (s *Session) routeDatagram(alias uint64, b []byte) *Request {
	// a datagram can overtake the SUBSCRIBE_OK or PUBLISH that names its alias, so it is kept briefly for it
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.aliases[alias]; ok {
		return r
	}
	now := time.Now()
	if t, ok := s.tombstones[alias]; ok && now.Sub(t) < tombstoneTTL {
		return nil
	}
	if s.earlyCount >= maxEarlyDatagrams {
		s.expireEarlyLocked(now)
		if s.earlyCount >= maxEarlyDatagrams {
			return nil
		}
	}
	s.early[alias] = append(s.early[alias], earlyDatagram{b: bytes.Clone(b), arrived: now})
	s.earlyCount++
	return nil
}

func (s *Session) expireEarlyLocked(now time.Time) {
	for alias, ds := range s.early {
		kept := ds[:0]
		for _, d := range ds {
			if now.Sub(d.arrived) < s.opts.UnknownAliasWait {
				kept = append(kept, d)
			}
		}
		s.earlyCount -= len(ds) - len(kept)
		if len(kept) == 0 {
			delete(s.early, alias)
		} else {
			s.early[alias] = kept
		}
	}
}

func (s *Session) deliverEarly(r *Request, early []earlyDatagram, registered time.Time) {
	d, ok := r.awaitData()
	if !ok {
		return
	}
	for _, e := range early {
		if registered.Sub(e.arrived) >= s.opts.UnknownAliasWait {
			continue
		}
		// it parsed as it arrived
		dg, _ := s.codec.ParseDatagram(e.b)
		d.HandleDatagram(r, &dg)
	}
}

func trackOf(m message.Message) message.FullTrackName {
	switch v := m.(type) {
	case *message.Subscribe:
		return v.Track
	case *message.Publish:
		return v.Track
	}
	return message.FullTrackName{}
}

func (s *Session) tombstone(alias uint64) {
	// the caller holds s.mu; objects for an ended subscription are discarded rather than parked
	now := time.Now()
	if len(s.tombstones) >= maxTombstones {
		for a, t := range s.tombstones {
			if now.Sub(t) > tombstoneTTL {
				delete(s.tombstones, a)
			}
		}
		if len(s.tombstones) >= maxTombstones {
			return
		}
	}
	s.tombstones[alias] = now
}

func (s *Session) routeAlias(alias uint64) *Request {
	s.mu.Lock()
	if r, ok := s.aliases[alias]; ok {
		s.mu.Unlock()
		return r
	}
	if t, ok := s.tombstones[alias]; ok && time.Since(t) < tombstoneTTL {
		s.mu.Unlock()
		return nil
	}
	if s.parked >= s.opts.MaxParked || s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	// a stream can overtake the SUBSCRIBE_OK or PUBLISH that names its alias, so it waits briefly for it
	ch := make(chan *Request, 1)
	s.waiting[alias] = append(s.waiting[alias], ch)
	s.parked++
	s.mu.Unlock()
	t := time.NewTimer(s.opts.UnknownAliasWait)
	defer t.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return nil
		}
		return r
	case <-t.C:
	}
	s.mu.Lock()
	waiters := s.waiting[alias]
	for i, w := range waiters {
		if w == ch {
			s.waiting[alias] = append(waiters[:i], waiters[i+1:]...)
			s.parked--
			break
		}
	}
	if len(s.waiting[alias]) == 0 {
		delete(s.waiting, alias)
	}
	s.mu.Unlock()
	select {
	case r, ok := <-ch:
		if ok {
			return r
		}
	default:
	}
	return nil
}

func (s *Session) routeFetch(id uint64, r *Request, order message.GroupOrder) {
	s.mu.Lock()
	s.fetches[id] = r
	s.mu.Unlock()
	r.mu.Lock()
	if r.fetchOrders == nil {
		r.fetchOrders = make(map[uint64]message.GroupOrder)
	}
	r.fetchOrders[id] = order
	r.mu.Unlock()
}

func (r *Request) awaitData() (DataHandler, bool) {
	// a fetch stream can trail its request stream, so handlers already set win over the request having ended
	select {
	case <-r.ready:
	default:
		select {
		case <-r.ready:
		case <-r.ctx.Done():
			return nil, false
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, r.data != nil
}

func (r *Request) dataNow() (DataHandler, bool) {
	select {
	case <-r.ready:
	default:
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, r.data != nil
}

func (s *Session) serveSubgroup(t uint64, rs ReceiveStream, br *bufio.Reader) {
	if !s.waitReady() {
		rs.CancelRead(uint64(message.ResetSessionClosed))
		return
	}
	sr, err := s.codec.ReadSubgroupHeader(t, br)
	if err != nil {
		if _, ok := errors.AsType[*message.SessionError](err); ok {
			s.fail(err)
		}
		return
	}
	r := s.routeAlias(sr.Header().TrackAlias)
	if r == nil {
		rs.CancelRead(uint64(message.ResetCancelled))
		return
	}
	d, ok := r.awaitData()
	if !ok {
		rs.CancelRead(uint64(message.ResetCancelled))
		return
	}
	in := &IncomingSubgroup{rs: rs, br: br, s: s, sr: sr}
	d.HandleSubgroup(r, in)
	in.finish()
}

func (s *Session) serveFetch(rs ReceiveStream, br *bufio.Reader) {
	if !s.waitReady() {
		rs.CancelRead(uint64(message.ResetSessionClosed))
		return
	}
	id, err := s.codec.ReadFetchHeader(br)
	if err != nil {
		return
	}
	s.mu.Lock()
	r := s.fetches[id]
	s.mu.Unlock()
	if r == nil {
		rs.CancelRead(uint64(message.ResetCancelled))
		return
	}
	d, ok := r.awaitData()
	if !ok {
		rs.CancelRead(uint64(message.ResetCancelled))
		return
	}
	in := &IncomingFetch{
		rs: rs, br: br, s: s, id: id,
		fr: s.codec.NewFetchReader(br, r.fetchOrder(id)),
	}
	d.HandleFetch(r, in)
	in.finish()
	s.mu.Lock()
	if s.fetches[id] == r {
		delete(s.fetches, id)
	}
	s.mu.Unlock()
}

func (s *Session) receiveDatagrams() {
	// one Datagram is reused for every arrival, since a handler may not keep it past the call
	dg := new(message.Datagram)
	for {
		b, err := s.conn.ReceiveDatagram(s.ctx)
		if err != nil {
			return
		}
		select {
		case <-s.setupDone:
		default:
			continue
		}
		if s.codec.IsPaddingDatagram(b) {
			continue
		}
		if *dg, err = s.codec.ParseDatagram(b); err != nil {
			s.fail(err)
			return
		}
		r := s.routeDatagram(dg.TrackAlias, b)
		if r == nil {
			continue
		}
		if d, ok := r.dataNow(); ok {
			d.HandleDatagram(r, dg)
		}
	}
}

type incoming struct {
	rs     ReceiveStream
	br     *bufio.Reader
	s      *Session
	remain uint64
	err    error
	ended  bool
}

const discardChunk = 1 << 20

func (in *incoming) skip() error {
	for in.remain > 0 {
		n := int(min(in.remain, discardChunk)) // #nosec G115 -- bounded by discardChunk
		if _, err := in.br.Discard(n); err != nil {
			return unexpected(err)
		}
		in.remain -= uint64(n)
	}
	return nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return streamErr(err)
}

// Read reads the current object's payload, returning io.EOF at its end
func (in *incoming) Read(p []byte) (int, error) {
	if in.remain == 0 {
		return 0, io.EOF
	}
	if uint64(len(p)) > in.remain {
		p = p[:in.remain] // #nosec G115 -- in.remain is below len(p) here
	}
	n, err := in.br.Read(p)
	in.remain -= uint64(n) // #nosec G115 -- a read count is never negative
	if err != nil {
		return n, unexpected(err)
	}
	return n, nil
}

// Cancel asks the publisher to stop sending the stream
func (in *incoming) Cancel(code message.ResetCode) {
	in.ended = true
	in.rs.CancelRead(uint64(code))
}

func (in *incoming) next(err error) error {
	if errors.Is(err, io.EOF) {
		in.ended = true
		return io.EOF
	}
	if _, ok := errors.AsType[*message.SessionError](err); ok {
		in.s.fail(err)
	}
	in.err = streamErr(err)
	return in.err
}

func (in *incoming) finish() {
	// a handler that stopped early frees the stream's flow control rather than leaving the peer blocked
	if !in.ended {
		in.rs.CancelRead(uint64(message.ResetCancelled))
	}
}

// IncomingSubgroup is a subgroup stream this endpoint receives
type IncomingSubgroup struct {
	incoming
	sr *codec.SubgroupReader
}

// Header returns the stream's subgroup header
func (in *IncomingSubgroup) Header() *message.SubgroupHeader {
	return in.sr.Header()
}

// Next skips any unread payload and returns the next object's header; io.EOF means the stream ended cleanly
func (in *IncomingSubgroup) Next() (message.SubgroupObject, error) {
	if err := in.skip(); err != nil {
		return message.SubgroupObject{}, in.next(err)
	}
	o, err := in.sr.Next()
	if err != nil {
		return o, in.next(err)
	}
	in.remain = o.PayloadLen
	return o, nil
}

// IncomingFetch is a fetch stream this endpoint receives
type IncomingFetch struct {
	incoming
	id uint64
	fr *codec.FetchReader
}

// RequestID returns the Request ID the stream answers: the FETCH, or the SUBSCRIBE or REQUEST_UPDATE of a fill
func (in *IncomingFetch) RequestID() uint64 {
	return in.id
}

// Next skips any unread payload and returns the next entry; io.EOF means the stream ended cleanly
func (in *IncomingFetch) Next() (message.FetchObject, error) {
	if err := in.skip(); err != nil {
		return message.FetchObject{}, in.next(err)
	}
	o, err := in.fr.Next()
	if err != nil {
		return o, in.next(err)
	}
	if o.EndOfRange == message.RangeEndNone {
		in.remain = o.PayloadLen
	}
	return o, nil
}

type outgoing struct {
	st     SendStream
	buf    []byte
	remain uint64
	mu     sync.Mutex
	closed bool
}

// Write writes the current object's payload
func (o *outgoing) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if uint64(len(p)) > o.remain {
		return 0, ErrPayloadOverrun
	}
	n, err := o.st.Write(p)
	o.remain -= uint64(n) // #nosec G115 -- a write count is never negative
	return n, streamErr(err)
}

// Close ends the stream with a FIN once the last object's payload is complete
func (o *outgoing) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.remain != 0 {
		return ErrPayloadPending
	}
	o.closed = true
	return o.st.Close()
}

// CloseWithTimeout ends the stream with a FIN, then resets it with DELIVERY_TIMEOUT if it is still open after d.
// The transport does not report full acknowledgement, so a delivered stream may see a reset its peer ignores.
func (o *outgoing) CloseWithTimeout(d time.Duration) error {
	if err := o.Close(); err != nil {
		return err
	}
	if d > 0 {
		time.AfterFunc(d, func() { o.st.CancelWrite(uint64(message.ResetDeliveryTimeout)) })
	}
	return nil
}

// Reset abandons the stream with code; a write blocked on flow control returns at once
func (o *outgoing) Reset(code message.ResetCode) {
	o.st.CancelWrite(uint64(code))
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

// ResetDelivered resets the stream with code while still delivering everything written so far (RESET_STREAM_AT)
func (o *outgoing) ResetDelivered(code message.ResetCode) {
	o.st.SetReliableBoundary()
	o.st.CancelWrite(uint64(code))
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

// SetWriteDeadline bounds blocked writes on the stream
func (o *outgoing) SetWriteDeadline(t time.Time) error {
	return o.st.SetWriteDeadline(t)
}

// Context is done once the stream is closed, reset, or stopped by the peer
func (o *outgoing) Context() context.Context {
	return o.st.Context()
}

// OutgoingSubgroup is a subgroup stream this endpoint sends
type OutgoingSubgroup struct {
	outgoing
	w *codec.SubgroupWriter
}

// WriteObject writes an object's header; exactly PayloadLen bytes of payload must follow through Write
func (o *OutgoingSubgroup) WriteObject(obj *message.SubgroupObject) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.remain != 0 {
		return ErrPayloadPending
	}
	b, err := o.w.AppendObject(o.buf[:0], obj)
	if err != nil {
		return err
	}
	o.buf = b
	if _, err = o.st.Write(b); err != nil {
		return streamErr(err)
	}
	o.remain = obj.PayloadLen
	return nil
}

// OpenSubgroup opens a subgroup stream for objects of this request's track; the header's alias is the request's
func (r *Request) OpenSubgroup(ctx context.Context, h message.SubgroupHeader, urgency int8,
	incremental bool,
) (*OutgoingSubgroup, error) {
	alias, ok := r.TrackAlias()
	if !ok {
		return nil, ErrNoTrackAlias
	}
	if r.ctx.Err() != nil {
		return nil, ErrRequestEnded
	}
	st, err := r.s.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	st.SetPriority(urgency, incremental)
	h.TrackAlias = alias
	b := r.s.codec.AppendSubgroupHeader(nil, &h)
	if _, err = st.Write(b); err != nil {
		st.CancelWrite(uint64(message.ResetInternalError))
		return nil, streamErr(err)
	}
	// a reset still delivers the header, so the subscriber can count the stream
	st.SetReliableBoundary()
	r.streams.Add(1)
	return &OutgoingSubgroup{st: st, buf: b[:0], w: r.s.codec.NewSubgroupWriter(h)}, nil
}

// OutgoingFetch is a fetch stream this endpoint sends
type OutgoingFetch struct {
	outgoing
	w *codec.FetchWriter
}

// WriteObject writes an entry; an object's PayloadLen bytes of payload must follow through Write
func (o *OutgoingFetch) WriteObject(obj *message.FetchObject) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.remain != 0 {
		return ErrPayloadPending
	}
	b, err := o.w.AppendObject(o.buf[:0], obj)
	if err != nil {
		return err
	}
	o.buf = b
	if _, err = o.st.Write(b); err != nil {
		return streamErr(err)
	}
	if obj.EndOfRange == message.RangeEndNone {
		o.remain = obj.PayloadLen
	}
	return nil
}

// OpenFetch opens the fetch stream answering requestID, which is this request's ID for a FETCH, or the Request
// ID of the SUBSCRIBE or REQUEST_UPDATE that asked for a fill
func (r *Request) OpenFetch(ctx context.Context, requestID uint64, order message.GroupOrder,
	urgency int8,
) (*OutgoingFetch, error) {
	if r.ctx.Err() != nil {
		return nil, ErrRequestEnded
	}
	st, err := r.s.conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	st.SetPriority(urgency, false)
	b := r.s.codec.AppendFetchHeader(nil, requestID)
	if _, err = st.Write(b); err != nil {
		st.CancelWrite(uint64(message.ResetInternalError))
		return nil, streamErr(err)
	}
	st.SetReliableBoundary()
	if r.kind == message.KindSubscribe || r.kind == message.KindPublish {
		r.streams.Add(1)
	}
	return &OutgoingFetch{st: st, buf: b[:0], w: r.s.codec.NewFetchWriter(order)}, nil
}

var datagramBuffers = sync.Pool{New: func() any { b := make([]byte, 0, 1500); return &b }}

const datagramQueueLen = 256

type queuedDatagram struct {
	buf      *[]byte
	deadline int64
}

func (s *Session) queueDatagram(q queuedDatagram) bool {
	// quic-go's SendDatagram blocks while its own queue is full, so one sender per session absorbs that wait
	s.dgOnce.Do(func() {
		s.dgQueue = make(chan queuedDatagram, datagramQueueLen)
		go s.sendDatagrams()
	})
	if s.ctx.Err() != nil {
		return false
	}
	select {
	case s.dgQueue <- q:
		return true
	default:
		return false
	}
}

func (s *Session) sendDatagrams() {
	for {
		select {
		case q := <-s.dgQueue:
			if q.deadline == 0 || time.Now().UnixNano() <= q.deadline {
				_ = s.conn.SendDatagram(*q.buf)
			}
			*q.buf = (*q.buf)[:0]
			datagramBuffers.Put(q.buf)
		case <-s.ctx.Done():
			return
		}
	}
}

// QueueDatagram queues an object of this request's track for a datagram without blocking. It is dropped with
// ErrDatagramDropped when the session's queue is full, and unsent once deadline passes (zero never passes).
func (r *Request) QueueDatagram(d *message.Datagram, deadline time.Time) error {
	alias, ok := r.TrackAlias()
	if !ok {
		return ErrNoTrackAlias
	}
	d.TrackAlias = alias
	bp := datagramBuffers.Get().(*[]byte)
	b, err := r.s.codec.AppendDatagram((*bp)[:0], d)
	if err != nil {
		datagramBuffers.Put(bp)
		return err
	}
	*bp = b
	var deadlineNano int64
	if !deadline.IsZero() {
		deadlineNano = deadline.UnixNano()
	}
	if !r.s.queueDatagram(queuedDatagram{buf: bp, deadline: deadlineNano}) {
		*bp = b[:0]
		datagramBuffers.Put(bp)
		return ErrDatagramDropped
	}
	return nil
}

// SendDatagram sends an object of this request's track in a datagram under the request's alias
func (r *Request) SendDatagram(d *message.Datagram) error {
	alias, ok := r.TrackAlias()
	if !ok {
		return ErrNoTrackAlias
	}
	d.TrackAlias = alias
	bp := datagramBuffers.Get().(*[]byte)
	defer datagramBuffers.Put(bp)
	b, err := r.s.codec.AppendDatagram((*bp)[:0], d)
	if err != nil {
		return err
	}
	*bp = b[:0]
	if err = r.s.conn.SendDatagram(b); err != nil {
		return fmt.Errorf("moq: datagram not sent: %w", err)
	}
	return nil
}
