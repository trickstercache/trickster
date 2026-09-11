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
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestPhaseConnSeparatesIdleAndActiveReadTimeouts(t *testing.T) {
	underlying := &scriptedPhaseConn{reads: []scriptedRead{
		{data: []byte{1}},
		{err: phaseTimeoutError{}},
		{err: phaseTimeoutError{}},
	}}
	const (
		readTimeout = 30 * time.Second
		idleTimeout = 5 * time.Minute
	)
	conn := newPhaseConn(underlying, 0, readTimeout, time.Minute, idleTimeout)
	conn.setReady()
	assertPhaseDeadline(t, underlying.readDeadline, idleTimeout)
	if underlying.readDeadlineCalls != 1 {
		t.Fatalf("idle deadline calls = %d, want 1", underlying.readDeadlineCalls)
	}

	buffer := make([]byte, 1)
	if n, err := conn.Read(buffer); n != 1 || err != nil {
		t.Fatalf("first command read = %d, %v", n, err)
	}
	assertPhaseDeadline(t, underlying.readDeadline, idleTimeout)
	if underlying.readDeadlineCalls != 1 {
		t.Fatalf("repeated idle deadline calls = %d, want 1", underlying.readDeadlineCalls)
	}

	if _, err := conn.Read(buffer); !isTimeout(err) {
		t.Fatalf("in-progress command read error = %v, want timeout", err)
	}
	assertPhaseDeadline(t, underlying.readDeadline, readTimeout)
	if underlying.readDeadlineCalls != 2 {
		t.Fatalf("active deadline calls = %d, want 2", underlying.readDeadlineCalls)
	}

	if _, err := conn.Write([]byte{2}); err != nil {
		t.Fatal(err)
	}
	assertPhaseDeadline(t, underlying.readDeadline, idleTimeout)
	if underlying.readDeadlineCalls != 3 || underlying.writeDeadlineCalls != 1 {
		t.Fatalf("post-write deadline calls = read %d, write %d, want 3 and 1",
			underlying.readDeadlineCalls, underlying.writeDeadlineCalls)
	}
	if _, err := conn.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	if underlying.readDeadlineCalls != 3 || underlying.writeDeadlineCalls != 1 {
		t.Fatalf("coalesced deadline calls = read %d, write %d, want 3 and 1",
			underlying.readDeadlineCalls, underlying.writeDeadlineCalls)
	}
	if _, err := conn.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("idle command read error = %v, want EOF", err)
	}
	assertPhaseDeadline(t, underlying.readDeadline, idleTimeout)
}

func assertPhaseDeadline(t *testing.T, deadline time.Time, want time.Duration) {
	t.Helper()
	remaining := time.Until(deadline)
	if remaining < want-time.Second || remaining > want+maxDeadlineRefreshSlack {
		t.Fatalf("read deadline remaining = %s, want approximately %s", remaining, want)
	}
}

type scriptedRead struct {
	data []byte
	err  error
}

type scriptedPhaseConn struct {
	reads              []scriptedRead
	readDeadline       time.Time
	readDeadlineCalls  int
	writeDeadlineCalls int
}

func (c *scriptedPhaseConn) Read(p []byte) (int, error) {
	if len(c.reads) == 0 {
		return 0, io.EOF
	}
	result := c.reads[0]
	c.reads = c.reads[1:]
	return copy(p, result.data), result.err
}

func (*scriptedPhaseConn) Write(p []byte) (int, error) { return len(p), nil }
func (*scriptedPhaseConn) Close() error                { return nil }
func (*scriptedPhaseConn) LocalAddr() net.Addr         { return phaseTestAddr("local") }
func (*scriptedPhaseConn) RemoteAddr() net.Addr        { return phaseTestAddr("remote") }
func (*scriptedPhaseConn) SetDeadline(time.Time) error { return nil }
func (c *scriptedPhaseConn) SetReadDeadline(deadline time.Time) error {
	c.readDeadline = deadline
	c.readDeadlineCalls++
	return nil
}
func (c *scriptedPhaseConn) SetWriteDeadline(time.Time) error {
	c.writeDeadlineCalls++
	return nil
}

type phaseTestAddr string

func (a phaseTestAddr) Network() string { return string(a) }
func (a phaseTestAddr) String() string  { return string(a) }

type phaseTimeoutError struct{}

func (phaseTimeoutError) Error() string   { return "i/o timeout" }
func (phaseTimeoutError) Timeout() bool   { return true }
func (phaseTimeoutError) Temporary() bool { return true }
