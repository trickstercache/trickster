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

package mysql

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	vtmysql "vitess.io/vitess/go/mysql"
)

// phaseConn applies one absolute deadline to the handshake, then per-operation
// deadlines after authentication. It also removes Vitess capabilities that the
// Trickster handler deliberately rejects.
type phaseConn struct {
	net.Conn
	ready            atomic.Bool
	awaitingCommand  atomic.Bool
	readTimeout      time.Duration
	idleTimeout      time.Duration
	writeTimeout     time.Duration
	firstWrite       atomic.Bool
	readDeadlineMtx  sync.Mutex
	readDeadline     atomic.Pointer[phaseDeadline]
	writeDeadlineMtx sync.Mutex
	writeDeadline    atomic.Pointer[phaseDeadline]
}

type phaseDeadline struct {
	timeout  time.Duration
	deadline time.Time
}

const maxDeadlineRefreshSlack = time.Second

func newPhaseConn(conn net.Conn, handshake, read, write, idle time.Duration) *phaseConn {
	c := &phaseConn{
		Conn: conn, readTimeout: read, idleTimeout: idle, writeTimeout: write,
	}
	if handshake > 0 {
		_ = conn.SetDeadline(time.Now().Add(handshake))
	}
	return c
}

func (c *phaseConn) setReady() {
	_ = c.Conn.SetDeadline(time.Time{})
	c.readDeadline.Store(nil)
	c.writeDeadline.Store(nil)
	c.ready.Store(true)
	c.awaitingCommand.Store(true)
	c.setIdleReadDeadline()
}

func (c *phaseConn) Read(p []byte) (int, error) {
	idleRead := c.ready.Load() && c.awaitingCommand.Load()
	if idleRead {
		c.setIdleReadDeadline()
	} else if c.ready.Load() && c.readTimeout > 0 {
		c.setReadDeadline(c.readTimeout)
	}
	n, err := c.Conn.Read(p)
	if n > 0 && idleRead {
		c.awaitingCommand.Store(false)
	}
	if n == 0 && idleRead && isTimeout(err) {
		// Vitess treats EOF as a normal client disconnect.
		return 0, io.EOF
	}
	return n, err
}

func (c *phaseConn) Write(p []byte) (int, error) {
	if !c.firstWrite.Swap(true) {
		p = maskUnsupportedCapabilities(p)
	}
	if c.ready.Load() && c.writeTimeout > 0 {
		c.setWriteDeadline(c.writeTimeout)
	}
	n, err := c.Conn.Write(p)
	if n > 0 && c.ready.Load() {
		c.awaitingCommand.Store(true)
		c.setIdleReadDeadline()
	}
	return n, err
}

func (c *phaseConn) setIdleReadDeadline() {
	if c.idleTimeout > 0 {
		c.setReadDeadline(c.idleTimeout)
	}
}

func (c *phaseConn) setReadDeadline(timeout time.Duration) {
	if deadlineReusable(c.readDeadline.Load(), timeout, time.Now()) {
		return
	}
	c.readDeadlineMtx.Lock()
	defer c.readDeadlineMtx.Unlock()
	now := time.Now()
	if deadlineReusable(c.readDeadline.Load(), timeout, now) {
		return
	}
	deadline := &phaseDeadline{timeout: timeout, deadline: coalescedDeadline(now, timeout)}
	if c.Conn.SetReadDeadline(deadline.deadline) == nil {
		c.readDeadline.Store(deadline)
	}
}

func (c *phaseConn) setWriteDeadline(timeout time.Duration) {
	if deadlineReusable(c.writeDeadline.Load(), timeout, time.Now()) {
		return
	}
	c.writeDeadlineMtx.Lock()
	defer c.writeDeadlineMtx.Unlock()
	now := time.Now()
	if deadlineReusable(c.writeDeadline.Load(), timeout, now) {
		return
	}
	deadline := &phaseDeadline{timeout: timeout, deadline: coalescedDeadline(now, timeout)}
	if c.Conn.SetWriteDeadline(deadline.deadline) == nil {
		c.writeDeadline.Store(deadline)
	}
}

func deadlineReusable(current *phaseDeadline, timeout time.Duration, now time.Time) bool {
	return current != nil && current.timeout == timeout && current.deadline.After(now.Add(timeout))
}

// coalescedDeadline adds a bounded refresh interval to the configured timeout.
// Repeated packet reads and writes can therefore reuse the installed deadline
// without ever shortening the configured period after the most recent I/O.
func coalescedDeadline(now time.Time, timeout time.Duration) time.Time {
	slack := min(timeout/8, maxDeadlineRefreshSlack)
	return now.Add(timeout + slack)
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	ok := errors.As(err, &netErr)
	return ok && netErr.Timeout()
}

// maskUnsupportedCapabilities rewrites only the server's initial handshake
// packet. The copy prevents mutation of Vitess's pooled packet buffer.
func maskUnsupportedCapabilities(packet []byte) []byte {
	if len(packet) < 5 || packet[4] != 10 {
		return packet
	}
	serverEnd := 5
	for serverEnd < len(packet) && packet[serverEnd] != 0 {
		serverEnd++
	}
	lower := serverEnd + 1 + 4 + 8 + 1
	upper := lower + 2 + 1 + 2
	if upper+2 > len(packet) {
		return packet
	}
	out := append([]byte(nil), packet...)
	capabilities := uint32(binary.LittleEndian.Uint16(out[lower:lower+2])) |
		uint32(binary.LittleEndian.Uint16(out[upper:upper+2]))<<16
	capabilities &^= vtmysql.CapabilityClientMultiStatements |
		vtmysql.CapabilityClientMultiResults
	// #nosec G115 -- this writes the intentionally selected low 16 capability bits.
	binary.LittleEndian.PutUint16(out[lower:lower+2], uint16(capabilities))
	binary.LittleEndian.PutUint16(out[upper:upper+2], uint16(capabilities>>16))
	return out
}
