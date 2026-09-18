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
	wg.Go(func() {
		s.pumpUpstream()
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
	scanner := newFrameScanner(clientObserver{s}, s.server.config.MaxMessageSizeBytes)
	buffer := pumpBuffers.Get().(*[]byte)
	defer pumpBuffers.Put(buffer)
	for {
		s.setClientReadDeadline(scanner)
		n, err := s.client.Read(*buffer)
		if n > 0 {
			if scanner.scan((*buffer)[:n]) != nil {
				s.server.countError(classProtocol)
				return
			}
			if !writeAll(s.upstream, (*buffer)[:n], s.server.config.WriteTimeout) {
				return
			}
		}
		if err != nil {
			// The idle deadline is armed by the origin pump and can land just
			// as a new request starts; that timeout is not the client's fault.
			if isTimeout(err) && n == 0 && scanner.atBoundary() && s.outstanding.Load() > 0 {
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

func (s *session) setClientReadDeadline(scanner *frameScanner) {
	var timeout time.Duration
	switch {
	case !scanner.atBoundary():
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
		n, err := s.upstream.Read(*buffer)
		if n > 0 {
			if scanner.scan((*buffer)[:n]) != nil {
				s.server.countError(classProtocol)
				return
			}
			if !writeAll(s.client, (*buffer)[:n], s.server.config.WriteTimeout) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func writeAll(conn net.Conn, chunk []byte, timeout time.Duration) bool {
	if timeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	_, err := conn.Write(chunk)
	return err == nil
}

type clientObserver struct{ s *session }

func (o clientObserver) message(typ byte, _ int) {
	switch typ {
	case msgQuery, msgSync, msgFunctionCall:
		if o.s.outstanding.Add(1) == 1 {
			o.s.requestStart.Store(time.Now().UnixNano())
		}
	}
}

func (clientObserver) firstByte(byte, byte) {}

type upstreamObserver struct{ s *session }

func (o upstreamObserver) message(typ byte, _ int) {
	switch typ {
	case msgDataRow:
		o.s.rows++
	case msgErrorResponse:
		o.s.failed = true
	}
}

func (o upstreamObserver) firstByte(_, txStatus byte) {
	o.s.observeReady([]byte{txStatus})
}

func (s *session) observeReady(body []byte) {
	if len(body) > 0 {
		s.txStatus.Store(uint32(body[0]))
	}
	if s.outstanding.Load() <= 0 {
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
	handles := &s.server.proxied
	if s.failed {
		handles = &s.server.failed
	}
	handles.requests.Inc()
	handles.elements.Add(float64(s.rows))
	handles.duration.Observe(time.Duration(nowNano - started).Seconds())
	s.rows, s.failed = 0, false
}
