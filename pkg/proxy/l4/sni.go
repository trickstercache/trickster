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

package l4

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"time"
)

// errPeeked ends the peeking handshake once the ClientHello has been read.
var errPeeked = errors.New("client hello read")

// ErrNotTLS indicates a connection whose first bytes are not a TLS ClientHello.
var ErrNotTLS = errors.New("connection did not begin with a TLS client hello")

// peekClientHello reads the client's TLS ClientHello without consuming it, returning the server
// name it offered and a connection that replays the bytes read, so the upstream sees them too.
func peekClientHello(conn net.Conn, timeout time.Duration) (string, net.Conn, error) {
	var buf bytes.Buffer
	var serverName string
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
	}
	// the standard library parses the hello and hands it over before negotiating a version, so
	// the floor never applies; the peek conn records what it reads and refuses to write, so no
	// ServerHello or alert reaches the client
	err := tls.Server(peekConn{r: io.TeeReader(conn, &buf)}, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			serverName = h.ServerName
			return nil, errPeeked
		},
	}).Handshake()
	_ = conn.SetReadDeadline(time.Time{})
	if !errors.Is(err, errPeeked) {
		if err == nil {
			err = ErrNotTLS
		}
		return "", nil, err
	}
	return serverName, &replayConn{Conn: conn, r: io.MultiReader(bytes.NewReader(buf.Bytes()), conn)}, nil
}

// peekConn is the read-only connection the peeking handshake runs over.
type peekConn struct {
	r io.Reader
}

func (c peekConn) Read(p []byte) (int, error)     { return c.r.Read(p) }
func (peekConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (peekConn) Close() error                     { return nil }
func (peekConn) LocalAddr() net.Addr              { return nil }
func (peekConn) RemoteAddr() net.Addr             { return nil }
func (peekConn) SetDeadline(time.Time) error      { return nil }
func (peekConn) SetReadDeadline(time.Time) error  { return nil }
func (peekConn) SetWriteDeadline(time.Time) error { return nil }

// replayConn serves the peeked bytes ahead of the connection's own.
type replayConn struct {
	net.Conn
	r io.Reader
}

func (c *replayConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// CloseWrite half-closes the underlying connection when it can be half-closed.
func (c *replayConn) CloseWrite() error {
	return closeWrite(c.Conn)
}
