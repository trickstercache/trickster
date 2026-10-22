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

// Package session runs one MoQ Transport session over a QUIC connection or a WebTransport session: the
// control streams and SETUP exchange, request streams and their state machines, and the data streams.
package session

import (
	"context"
	"fmt"
	"io"
	"time"
)

// StreamError is a stream reset or STOP_SENDING carrying an application error code
type StreamError struct {
	Code   uint64
	Remote bool
}

func (e *StreamError) Error() string {
	side := "local"
	if e.Remote {
		side = "remote"
	}
	return fmt.Sprintf("moq: stream canceled by %s with code %#x", side, e.Code)
}

// CloseError is the application error a connection closed with
type CloseError struct {
	Code   uint64
	Reason string
	Remote bool
}

func (e *CloseError) Error() string {
	side := "local"
	if e.Remote {
		side = "remote"
	}
	return fmt.Sprintf("moq: session closed by %s with code %#x: %s", side, e.Code, e.Reason)
}

// SendStream is the sending half of a stream
type SendStream interface {
	io.Writer
	// Close ends the stream with a FIN
	Close() error
	// CancelWrite ends the stream with RESET_STREAM
	CancelWrite(code uint64)
	SetWriteDeadline(t time.Time) error
	// SetPriority sets the RFC 9218 urgency (0 most urgent, to 7) and incremental flag
	SetPriority(urgency int8, incremental bool)
	// SetReliableBoundary marks what is written so far as delivered reliably even if the stream is reset
	SetReliableBoundary()
	// Context is done once the sending half is closed, canceled, or stopped by the peer
	Context() context.Context
}

// ReceiveStream is the receiving half of a stream
type ReceiveStream interface {
	io.Reader
	// CancelRead asks the peer to stop sending with STOP_SENDING
	CancelRead(code uint64)
	SetReadDeadline(t time.Time) error
}

// Stream is a bidirectional stream
type Stream interface {
	SendStream
	ReceiveStream
}

// Conn is the transport a session runs over: a QUIC connection or a WebTransport session
type Conn interface {
	OpenStreamSync(ctx context.Context) (Stream, error)
	OpenUniStreamSync(ctx context.Context) (SendStream, error)
	AcceptStream(ctx context.Context) (Stream, error)
	AcceptUniStream(ctx context.Context) (ReceiveStream, error)
	SendDatagram(b []byte) error
	ReceiveDatagram(ctx context.Context) ([]byte, error)
	CloseWithError(code uint64, reason string) error
	// Context is done when the connection ends
	Context() context.Context
	// Cause returns why the connection ended once Context is done; an application close is a *CloseError
	Cause() error
}
