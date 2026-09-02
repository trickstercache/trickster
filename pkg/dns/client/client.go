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

// Package client is a minimal DNS stub resolver covering only what Trickster
// needs: querying a specific server for A, AAAA and SRV records and reading
// the TTLs of the answers, over UDP with automatic retry on TCP.
package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"time"
)

const (
	// DefaultTimeout bounds a single exchange, including connection setup
	DefaultTimeout = 5 * time.Second
	// DefaultUDPSize is the EDNS0 buffer size advertised on UDP queries; it
	// is the size the DNS flag day recommends for avoiding fragmentation
	DefaultUDPSize = 1232
	// minUDPSize is the response ceiling of DNS over UDP without EDNS0
	minUDPSize = 512
	maxTCPSize = 65535
	netUDP     = "udp"
	netTCP     = "tcp"
)

// Client queries a single DNS server. The zero value queries over UDP with
// EDNS0 enabled and is ready to use.
type Client struct {
	// Net is the transport for the initial query, "udp" (the default) or
	// "tcp". UDP answers that come back truncated are retried on TCP.
	Net string
	// Timeout bounds each exchange; DefaultTimeout applies when unset
	Timeout time.Duration
	// UDPSize is the EDNS0 buffer size advertised on UDP queries. Unset uses
	// DefaultUDPSize; a negative value disables EDNS0.
	UDPSize int
	// Dialer, when set, establishes the connection to the server
	Dialer *net.Dialer
}

// Query asks server (host:port) for the records of type t at name, and
// returns the response regardless of its response code
func (c *Client) Query(ctx context.Context, server, name string, t Type) (*Msg, error) {
	m := &Msg{
		ID:               messageID(),
		RecursionDesired: true,
		Questions: []Question{{
			Name:  Fqdn(name),
			Type:  t,
			Class: ClassINET,
		}},
	}
	return c.Exchange(ctx, m, server)
}

// Exchange sends m to server (host:port) and returns its response, retrying
// over TCP when a UDP answer comes back truncated
func (c *Client) Exchange(ctx context.Context, m *Msg, server string) (*Msg, error) {
	network := c.Net
	if network == "" {
		network = netUDP
	}
	r, err := c.exchange(ctx, network, m, server)
	if err != nil || network != netUDP || !r.Truncated {
		return r, err
	}
	// a truncated answer is incomplete by definition, and TCP has no such
	// size ceiling, so the retry is the only way to see the full answer
	return c.exchange(ctx, netTCP, m, server)
}

func (c *Client) exchange(ctx context.Context, network string, m *Msg,
	server string,
) (*Msg, error) {
	q := *m
	q.UDPSize = 0
	if network == netUDP {
		q.UDPSize = c.udpSize()
	}
	wire, err := q.Pack()
	if err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := c.Dialer
	if d == nil {
		d = &net.Dialer{}
	}
	conn, err := d.DialContext(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if network == netTCP {
		return exchangeTCP(conn, &q, wire)
	}
	return exchangeUDP(conn, &q, wire, int(q.UDPSize))
}

// udpSize resolves the advertised EDNS0 buffer size; a negative configured
// value disables EDNS0 by returning zero
func (c *Client) udpSize() uint16 {
	switch {
	case c.UDPSize < 0:
		return 0
	case c.UDPSize == 0:
		return DefaultUDPSize
	case c.UDPSize > maxTCPSize:
		return maxTCPSize
	default:
		return uint16(c.UDPSize)
	}
}

func exchangeUDP(conn net.Conn, q *Msg, wire []byte, bufSize int) (*Msg, error) {
	if _, err := conn.Write(wire); err != nil {
		return nil, err
	}
	buf := make([]byte, max(bufSize, minUDPSize))
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		r := &Msg{}
		// datagrams that are malformed or answer a different question are
		// off-path noise; the deadline bounds the wait for the real answer
		if err := r.Unpack(buf[:n]); err != nil || !r.answers(q) {
			continue
		}
		return r, nil
	}
}

func exchangeTCP(conn net.Conn, q *Msg, wire []byte) (*Msg, error) {
	n := len(wire)
	if n > maxTCPSize {
		return nil, ErrLongMessage
	}
	framed := make([]byte, 2, n+2)
	binary.BigEndian.PutUint16(framed, uint16(n))
	if _, err := conn.Write(append(framed, wire...)); err != nil {
		return nil, err
	}
	var sz [2]byte
	if _, err := io.ReadFull(conn, sz[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(sz[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	r := &Msg{}
	if err := r.Unpack(buf); err != nil {
		return nil, err
	}
	if !r.answers(q) {
		return nil, ErrNoResponse
	}
	return r, nil
}

// messageID returns an unpredictable query ID, which together with the
// question match in answers() is the stub resolver's spoofing defense
func messageID() uint16 {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint16(b[:])
}
