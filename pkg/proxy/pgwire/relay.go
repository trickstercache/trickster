/*
 * Copyright 2018 The Trickster Authors
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
package pgwire

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

func (s *session) relay() {
	var (
		once sync.Once
		wg   sync.WaitGroup
	)
	// Both the origin pump and a cache fetch read the origin through this one
	// reader, so bytes buffered by either are never lost to the other.
	s.upstreamReader = bufio.NewReaderSize(s.upstream, upstreamReaderSize)
	wg.Go(func() {
		s.pumpUpstream()
		s.handoff.finish()
		once.Do(func() { s.teardown(false) })
	})
	s.pumpClient()
	once.Do(func() { s.teardown(true) })
	wg.Wait()
}

func (s *session) teardown(clientLeft bool) {
	// closes the session. A client that leaves with a request in flight
	// has its statement canceled, so the origin does not finish abandoned work.
	if clientLeft && s.outstanding.Load() > 0 && s.pid != 0 && !s.server.isClosing() {
		ctx, cancel := context.WithTimeout(context.Background(), s.server.cancelBudget())
		if s.server.config.cancelUpstream(ctx, s.realPID, s.realSecret) == nil {
			s.server.countEvent(eventCanceled)
		}
		cancel()
	}
	s.close()
}

func (s *session) pumpClient() {
	stream := s.newClientStream()
	buffer := pumpBuffers.Get().(*[]byte)
	defer pumpBuffers.Put(buffer)
	for {
		s.setClientReadDeadline(stream)
		n, err := s.client.Read(*buffer)
		if n > 0 {
			if relayErr := stream.relay((*buffer)[:n]); relayErr != nil {
				if errors.Is(relayErr, errFrameLength) {
					s.server.countError(classProtocol)
				}
				return
			}
		}
		if err != nil {
			// The idle deadline is armed by the origin pump and can land just
			// as a new request starts; that timeout is not the client's fault.
			if isTimeout(err) && n == 0 && stream.atBoundary() && s.outstanding.Load() > 0 {
				continue
			}
			return
		}
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (s *session) setClientReadDeadline(stream clientStream) {
	var timeout time.Duration
	switch {
	case !stream.atBoundary():
		timeout = s.server.config.ReadTimeout
	case s.outstanding.Load() == 0:
		timeout = s.server.config.IdleTimeout
	}
	// With a request in flight the client is legitimately silent.
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	_ = s.client.SetReadDeadline(deadline)
}

func (s *session) pumpUpstream() {
	scanner := newFrameScanner(upstreamObserver{s}, pgMaxMessageBody)
	buffer := pumpBuffers.Get().(*[]byte)
	defer pumpBuffers.Put(buffer)
	for {
		n, err := s.upstreamReader.Read(*buffer)
		if n > 0 {
			if scanner.scan((*buffer)[:n]) != nil {
				s.server.countError(classProtocol)
				return
			}
			if !writeAll(s.client, (*buffer)[:n], s.server.config.WriteTimeout) {
				return
			}
		}
		if err != nil && (!isTimeout(err) || !s.handoff.requested()) {
			return
		}
		if !s.handoff.requested() {
			continue
		}
		if !scanner.atBoundary() {
			// the interrupting deadline landed inside a message; finish it first
			_ = s.upstream.SetReadDeadline(time.Now().Add(s.server.config.ReadTimeout))
			continue
		}
		s.handoff.park()
	}
}

type upstreamHandoff struct {
	mtx      sync.Mutex
	cond     *sync.Cond
	wanted   bool
	parked   bool
	finished bool
}

func (h *upstreamHandoff) init() { h.cond = sync.NewCond(&h.mtx) }

func (h *upstreamHandoff) requested() bool {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return h.wanted
}

func (h *upstreamHandoff) park() {
	h.mtx.Lock()
	h.parked = true
	h.cond.Broadcast()
	for h.wanted {
		h.cond.Wait()
	}
	h.parked = false
	h.mtx.Unlock()
}

func (h *upstreamHandoff) finish() {
	h.mtx.Lock()
	h.finished = true
	h.cond.Broadcast()
	h.mtx.Unlock()
}

func (s *session) acquireUpstream() bool {
	// A past read deadline wakes a pump blocked in Read. It parks only between messages,
	// so the borrowed connection is at a boundary and the client saw nothing half-written.
	h := &s.handoff
	h.mtx.Lock()
	h.wanted = true
	h.mtx.Unlock()
	_ = s.upstream.SetReadDeadline(time.Unix(1, 0))
	h.mtx.Lock()
	defer h.mtx.Unlock()
	for !h.parked && !h.finished {
		h.cond.Wait()
	}
	if h.parked {
		// the interrupting deadline has done its work; the borrower reads next
		_ = s.upstream.SetReadDeadline(time.Time{})
	}
	return h.parked
}

func (s *session) releaseUpstream() {
	// clears the deadline before waking the pump: a pump that
	// cleared it itself could wipe the interrupt of the next acquire.
	_ = s.upstream.SetReadDeadline(time.Time{})
	h := &s.handoff
	h.mtx.Lock()
	h.wanted = false
	h.cond.Broadcast()
	h.mtx.Unlock()
}

func writeAll(conn net.Conn, chunk []byte, timeout time.Duration) bool {
	if timeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	_, err := conn.Write(chunk)
	return err == nil
}

type clientStream interface {
	relay(chunk []byte) error
	atBoundary() bool
}

func (s *session) newClientStream() clientStream {
	forward := func(chunk []byte) error {
		if !writeAll(s.upstream, chunk, s.server.config.WriteTimeout) {
			return net.ErrClosed
		}
		return nil
	}
	// A backend that only proxies keeps the bulk relay; one whose statements
	// are inspected holds each complete statement for the gate first.
	if s.tracker == nil {
		return &bulkStream{
			scanner: newFrameScanner(clientObserver{s}, s.server.config.MaxMessageSizeBytes), forward: forward,
		}
	}
	return &clientSplitter{
		s: s, maxBody: s.server.config.MaxMessageSizeBytes,
		maxHeld: s.server.config.MaxQuerySizeBytes, forward: forward,
	}
}

type bulkStream struct {
	scanner *frameScanner
	forward func([]byte) error
}

func (b *bulkStream) atBoundary() bool { return b.scanner.atBoundary() }

func (b *bulkStream) relay(chunk []byte) error {
	if err := b.scanner.scan(chunk); err != nil {
		return err
	}
	return b.forward(chunk)
}

func (c *clientSplitter) relay(chunk []byte) error { return c.process(chunk) }

func (s *session) countRequest(typ byte) {
	// these are the client messages that each end in exactly one ReadyForQuery
	switch typ {
	case msgQuery, msgSync, msgFunctionCall:
		if s.outstanding.Add(1) == 1 {
			s.requestStart.Store(time.Now().UnixNano())
		}
	}
}

type clientObserver struct{ s *session }

func (o clientObserver) message(typ byte, _ int) bool {
	o.s.countRequest(typ)
	return false
}

func (clientObserver) firstByte(byte, byte) {}

func (clientObserver) body(byte, []byte) {}

type upstreamObserver struct{ s *session }

func (o upstreamObserver) message(typ byte, _ int) bool {
	switch typ {
	case msgDataRow:
		o.s.rows++
	case msgErrorResponse:
		o.s.failed = true
	case msgEmptyQuery:
		o.s.emptyQuery = true
	case msgCommandComplete:
		o.s.completed = true
	case msgParameterStatus:
		return o.s.tracker != nil
	}
	return false
}

func (o upstreamObserver) body(_ byte, body []byte) { o.s.observeParameterStatus(body) }

func (o upstreamObserver) firstByte(_, txStatus byte) {
	o.s.observeReady([]byte{txStatus})
}

func (s *session) observeReady(body []byte) {
	if len(body) > 0 {
		s.txStatus.Store(uint32(body[0]))
	}
	if s.tracker != nil {
		s.tracker.ready(s.failed)
	}
	if s.outstanding.Load() <= 0 {
		s.resetRequest()
		return
	}
	nowNano := time.Now().UnixNano()
	started := s.requestStart.Swap(nowNano)
	if s.outstanding.Add(-1) == 0 {
		// The client pump is blocked in a read that has no deadline.
		if idle := s.server.config.IdleTimeout; idle > 0 {
			_ = s.client.SetReadDeadline(time.Unix(0, nowNano).Add(idle))
		}
	}
	// a driver's keepalive, such as the "-- ping" pgx sends on pool checkout, ran
	// no statement; counting it would pair every cached query with a relayed one
	if keepalive := s.emptyQuery && !s.completed && !s.failed && s.rows == 0; !keepalive {
		handles := &s.server.proxied
		if s.failed {
			handles = &s.server.failed
		}
		handles.requests.Inc()
		handles.elements.Add(float64(s.rows))
		handles.duration.Observe(time.Duration(nowNano - started).Seconds())
	}
	s.resetRequest()
}

func (s *session) resetRequest() {
	s.rows, s.failed, s.emptyQuery, s.completed = 0, false, false, false
}
