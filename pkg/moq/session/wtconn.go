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
	"math"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	wtFirstCode = 0x52e4a40fa8db
	wtLastCode  = 0x52e5ac983162
	wtSkip      = 0x1f
	wtReserved  = 0x21
)

var errSessionGone = errors.New("moq: WebTransport session gone")

type wtConn struct {
	s *webtransport.Session
}

// FromWebTransport adapts a WebTransport session that negotiated a MoQ protocol. WebTransport streams take no
// RFC 9218 urgency and no reliable reset boundary, so SetPriority and SetReliableBoundary do nothing on them.
func FromWebTransport(s *webtransport.Session) Conn {
	return &wtConn{s: s}
}

func (w *wtConn) OpenStreamSync(ctx context.Context) (Stream, error) {
	s, err := w.s.OpenStreamSync(ctx)
	if err != nil {
		return nil, wtErr(err)
	}
	return &wtStream{s: s}, nil
}

func (w *wtConn) OpenUniStreamSync(ctx context.Context) (SendStream, error) {
	s, err := w.s.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, wtErr(err)
	}
	return &wtSendStream{s: s}, nil
}

func (w *wtConn) AcceptStream(ctx context.Context) (Stream, error) {
	s, err := w.s.AcceptStream(ctx)
	if err != nil {
		return nil, wtErr(err)
	}
	return &wtStream{s: s}, nil
}

func (w *wtConn) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	s, err := w.s.AcceptUniStream(ctx)
	if err != nil {
		return nil, wtErr(err)
	}
	return &wtReceiveStream{s: s}, nil
}

func (w *wtConn) SendDatagram(b []byte) error {
	return wtErr(w.s.SendDatagram(b))
}

func (w *wtConn) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	b, err := w.s.ReceiveDatagram(ctx)
	return b, wtErr(err)
}

func (w *wtConn) CloseWithError(code uint64, reason string) error {
	return w.s.CloseWithError(webtransport.SessionErrorCode(wtCode(code, uint64(message.CodeInternalError))), reason)
}

func (w *wtConn) Context() context.Context {
	return w.s.Context()
}

func (w *wtConn) Cause() error {
	if w.s.Context().Err() == nil {
		return nil
	}
	// a closed session answers every open with the error it closed with and opens nothing, which is the only
	// place webtransport-go reports that error
	_, err := w.s.OpenUniStream()
	return wtErr(err)
}

func wtCode(code, otherwise uint64) uint32 {
	// WebTransport codes are 32 bits wide; every code MoQ defines fits
	if code > math.MaxUint32 {
		code = otherwise
	}
	return uint32(code) // #nosec G115 -- bounded above
}

func wtErr(err error) error {
	if err == nil {
		return nil
	}
	if se, ok := errors.AsType[*webtransport.StreamError](err); ok {
		return &StreamError{Code: uint64(se.ErrorCode), Remote: se.Remote}
	}
	if se, ok := errors.AsType[*webtransport.SessionError](err); ok {
		return &CloseError{Code: uint64(se.ErrorCode), Reason: se.Message, Remote: se.Remote}
	}
	if he, ok := errors.AsType[*http3.Error](err); ok {
		return &CloseError{Code: uint64(he.ErrorCode), Reason: he.ErrorMessage, Remote: he.Remote}
	}
	return streamErr(err)
}

func streamCode(code uint64) uint64 {
	// a WebTransport stream reset seen through its QUIC stream carries the code mapped into the HTTP/3 error space,
	// skipping each 0x1f * N + 0x21 (draft-ietf-webtrans-http3, section 4.4)
	if code < wtFirstCode || code > wtLastCode || (code-wtReserved)%wtSkip == 0 {
		return code
	}
	shifted := code - wtFirstCode
	return shifted - shifted/wtSkip
}

type wtSendStream struct {
	s *webtransport.SendStream
}

func (w *wtSendStream) Write(p []byte) (int, error) {
	n, err := w.s.Write(p)
	return n, wtErr(err)
}

func (w *wtSendStream) Close() error { return wtErr(w.s.Close()) }
func (w *wtSendStream) CancelWrite(code uint64) {
	w.s.CancelWrite(webtransport.StreamErrorCode(wtCode(code, 0)))
}
func (w *wtSendStream) SetPriority(int8, bool)             {}
func (w *wtSendStream) SetReliableBoundary()               {}
func (w *wtSendStream) Context() context.Context           { return w.s.Context() }
func (w *wtSendStream) SetWriteDeadline(t time.Time) error { return w.s.SetWriteDeadline(t) }

type wtReceiveStream struct {
	s *webtransport.ReceiveStream
}

func (w *wtReceiveStream) Read(p []byte) (int, error) {
	n, err := w.s.Read(p)
	return n, wtErr(err)
}

func (w *wtReceiveStream) CancelRead(code uint64) {
	w.s.CancelRead(webtransport.StreamErrorCode(wtCode(code, 0)))
}
func (w *wtReceiveStream) SetReadDeadline(t time.Time) error { return w.s.SetReadDeadline(t) }

type wtStream struct {
	s *webtransport.Stream
}

func (w *wtStream) Write(p []byte) (int, error) {
	n, err := w.s.Write(p)
	return n, wtErr(err)
}

func (w *wtStream) Read(p []byte) (int, error) {
	n, err := w.s.Read(p)
	return n, wtErr(err)
}

func (w *wtStream) Close() error { return wtErr(w.s.Close()) }
func (w *wtStream) CancelWrite(code uint64) {
	w.s.CancelWrite(webtransport.StreamErrorCode(wtCode(code, 0)))
}

func (w *wtStream) CancelRead(code uint64) {
	w.s.CancelRead(webtransport.StreamErrorCode(wtCode(code, 0)))
}
func (w *wtStream) SetPriority(int8, bool) {}
func (w *wtStream) SetReliableBoundary()   {}
func (w *wtStream) Context() context.Context {
	return w.s.Context()
}
func (w *wtStream) SetWriteDeadline(t time.Time) error { return w.s.SetWriteDeadline(t) }
func (w *wtStream) SetReadDeadline(t time.Time) error  { return w.s.SetReadDeadline(t) }
