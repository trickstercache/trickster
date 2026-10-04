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
	"context"
	"errors"
	"time"

	"github.com/quic-go/quic-go"
)

type quicConn struct {
	c *quic.Conn
}

// FromQUIC adapts a QUIC connection that negotiated a MoQ ALPN
func FromQUIC(c *quic.Conn) Conn {
	return &quicConn{c: c}
}

func (q *quicConn) OpenStreamSync(ctx context.Context) (Stream, error) {
	s, err := q.c.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &quicStream{s: s}, nil
}

func (q *quicConn) OpenUniStreamSync(ctx context.Context) (SendStream, error) {
	s, err := q.c.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &quicSendStream{s: s}, nil
}

func (q *quicConn) AcceptStream(ctx context.Context) (Stream, error) {
	s, err := q.c.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &quicStream{s: s}, nil
}

func (q *quicConn) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	s, err := q.c.AcceptUniStream(ctx)
	if err != nil {
		return nil, err
	}
	return &quicReceiveStream{s: s}, nil
}

func (q *quicConn) SendDatagram(b []byte) error {
	return q.c.SendDatagram(b)
}

func (q *quicConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return q.c.ReceiveDatagram(ctx)
}

func (q *quicConn) CloseWithError(code uint64, reason string) error {
	return q.c.CloseWithError(quic.ApplicationErrorCode(code), reason)
}

func (q *quicConn) Context() context.Context {
	return q.c.Context()
}

func (q *quicConn) Cause() error {
	return streamErr(context.Cause(q.c.Context()))
}

func streamErr(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := errors.AsType[*quic.StreamError](err); ok {
		return &StreamError{Code: uint64(se.ErrorCode), Remote: se.Remote}
	}
	if ae, ok := errors.AsType[*quic.ApplicationError](err); ok {
		return &CloseError{Code: uint64(ae.ErrorCode), Reason: ae.ErrorMessage, Remote: ae.Remote}
	}
	return err
}

type quicSendStream struct {
	s *quic.SendStream
}

func (q *quicSendStream) Write(p []byte) (int, error) {
	n, err := q.s.Write(p)
	return n, streamErr(err)
}

func (q *quicSendStream) Close() error                       { return q.s.Close() }
func (q *quicSendStream) CancelWrite(code uint64)            { q.s.CancelWrite(quic.StreamErrorCode(code)) }
func (q *quicSendStream) SetPriority(u int8, incr bool)      { q.s.SetPriority(u, incr) }
func (q *quicSendStream) SetReliableBoundary()               { q.s.SetReliableBoundary() }
func (q *quicSendStream) Context() context.Context           { return q.s.Context() }
func (q *quicSendStream) SetWriteDeadline(t time.Time) error { return q.s.SetWriteDeadline(t) }

type quicReceiveStream struct {
	s *quic.ReceiveStream
}

func (q *quicReceiveStream) Read(p []byte) (int, error) {
	n, err := q.s.Read(p)
	return n, streamErr(err)
}

func (q *quicReceiveStream) CancelRead(code uint64)            { q.s.CancelRead(quic.StreamErrorCode(code)) }
func (q *quicReceiveStream) SetReadDeadline(t time.Time) error { return q.s.SetReadDeadline(t) }

type quicStream struct {
	s *quic.Stream
}

func (q *quicStream) Write(p []byte) (int, error) {
	n, err := q.s.Write(p)
	return n, streamErr(err)
}

func (q *quicStream) Read(p []byte) (int, error) {
	n, err := q.s.Read(p)
	return n, streamErr(err)
}

func (q *quicStream) Close() error            { return q.s.Close() }
func (q *quicStream) CancelWrite(code uint64) { q.s.CancelWrite(quic.StreamErrorCode(code)) }

func (q *quicStream) CancelRead(code uint64)             { q.s.CancelRead(quic.StreamErrorCode(code)) }
func (q *quicStream) SetPriority(u int8, incr bool)      { q.s.SetPriority(u, incr) }
func (q *quicStream) SetReliableBoundary()               { q.s.SetReliableBoundary() }
func (q *quicStream) Context() context.Context           { return q.s.Context() }
func (q *quicStream) SetWriteDeadline(t time.Time) error { return q.s.SetWriteDeadline(t) }
func (q *quicStream) SetReadDeadline(t time.Time) error  { return q.s.SetReadDeadline(t) }
