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

package integration

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

// streamEcho is a tcp backend that answers each line with its own name and counts the
// connections it accepted; it can be taken down and brought back on the same port
type streamEcho struct {
	name     string
	addr     string
	mu       sync.Mutex
	ln       net.Listener
	accepted atomic.Int64
}

func newStreamEcho(t *testing.T, name string) *streamEcho {
	t.Helper()
	e := &streamEcho{name: name}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	e.addr = ln.Addr().String()
	e.serve(ln)
	t.Cleanup(e.stop)
	return e
}

func (e *streamEcho) serve(ln net.Listener) {
	e.mu.Lock()
	e.ln = ln
	e.mu.Unlock()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			e.accepted.Add(1)
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					_, _ = fmt.Fprintf(conn, "%s:%s\n", e.name, strings.TrimSpace(line))
				}
			}()
		}
	}()
}

func (e *streamEcho) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ln != nil {
		_ = e.ln.Close()
		e.ln = nil
	}
}

func (e *streamEcho) restart(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		ln, err := net.Listen("tcp", e.addr)
		if err != nil {
			return false
		}
		e.serve(ln)
		return true
	}, 5*time.Second, 50*time.Millisecond, "could not listen on %s again", e.addr)
}

func udpEchoBackend(t *testing.T, name string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo([]byte(name+":"+string(buf[:n])), from)
		}
	}()
	return pc.LocalAddr().String()
}

// streamLB is a running Trickster with one stream listener bound to one ALB
type streamLB struct {
	cfgPath  string
	addr     string
	protocol string
	ports    []int
}

// streamConfig writes the configuration: members are name -> address, and alb is the body of
// the ALB's alb block below its pool, already indented; the members named as backups stand by
func (s *streamLB) write(t *testing.T, members [][2]string, memberExtra, alb string, backups ...string) {
	t.Helper()
	var sb strings.Builder
	// the stream listener belongs with the preamble's own, ahead of its other sections
	relay := fmt.Sprintf("listeners:\n  relay:\n    address: 127.0.0.1\n    protocol: %s\n    port: %d\n"+
		"    stream:\n      connect_timeout: 2s\n", s.protocol, s.ports[3])
	sb.WriteString(strings.Replace(promstub.Preamble(s.ports[0], s.ports[1], s.ports[2]), "listeners:\n", relay, 1))
	sb.WriteString("backends:\n")
	sb.WriteString("  none:\n    provider: rp\n    origin_url: http://127.0.0.1:1\n")
	for _, m := range members {
		fmt.Fprintf(&sb, "  %s:\n    provider: rp\n    origin_url: %s://%s\n    listener_names: [relay]\n",
			m[0], map[bool]string{true: "udp", false: "tcp"}[s.protocol == "udp"], m[1])
		sb.WriteString(memberExtra)
	}
	sb.WriteString("  lb:\n    provider: alb\n    listener_names: [relay]\n    alb:\n")
	sb.WriteString(alb)
	sb.WriteString("      pool:\n")
	for _, m := range members {
		if slices.Contains(backups, m[0]) {
			fmt.Fprintf(&sb, "        - {name: %s, backup: true}\n", m[0])
			continue
		}
		fmt.Fprintf(&sb, "        - %s\n", m[0])
	}
	require.NoError(t, os.WriteFile(s.cfgPath, []byte(sb.String()), 0o644))
}

func startStreamLB(t *testing.T, protocol string, members [][2]string, memberExtra, alb string,
	backups ...string,
) *streamLB {
	t.Helper()
	ports, release := portutil.Reserve(t, 4)
	s := &streamLB{
		cfgPath: filepath.Join(t.TempDir(), "trickster.yaml"), protocol: protocol, ports: ports,
		addr: fmt.Sprintf("127.0.0.1:%d", ports[3]),
	}
	s.write(t, members, memberExtra, alb, backups...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	runTrickster(t, ctx, "-config", s.cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", ports[1]))
	return s
}

// ask opens a connection, sends one line and returns which member answered, or "" when the
// connection was refused; the connection is returned open
func (s *streamLB) ask(t *testing.T) (string, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", s.addr, 2*time.Second)
	require.NoError(t, err)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("hi\n")); err != nil {
		_ = conn.Close()
		return "", nil
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return "", nil
	}
	name, _, _ := strings.Cut(reply, ":")
	return name, conn
}

func (s *streamLB) askAndClose(t *testing.T) string {
	t.Helper()
	name, conn := s.ask(t)
	if conn != nil {
		_ = conn.Close()
	}
	return name
}

func tcpMembers(t *testing.T, names ...string) ([][2]string, map[string]*streamEcho) {
	t.Helper()
	members := make([][2]string, len(names))
	echoes := make(map[string]*streamEcho, len(names))
	for i, n := range names {
		echoes[n] = newStreamEcho(t, n)
		members[i] = [2]string{n, echoes[n].addr}
	}
	return members, echoes
}

// every mechanism that commits to one member balances tcp connections across the whole pool
func TestStreamLBMechanisms(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster per mechanism; skipping in -short mode")
	}
	for _, mech := range []string{"rr", "p2c", "lc", "lt"} {
		t.Run(mech, func(t *testing.T) {
			members, _ := tcpMembers(t, "a", "b", "c")
			s := startStreamLB(t, "tcp", members, "", "      mechanism: "+mech+"\n")
			seen := make(map[string]int)
			var held []net.Conn
			for range 60 {
				name, conn := s.ask(t)
				require.NotEmpty(t, name, "%s refused a connection with every member up", mech)
				seen[name]++
				// keep some work in flight, which is what the load-aware mechanisms balance
				held = append(held, conn)
				if len(held) > 6 {
					_ = held[0].Close()
					held = held[1:]
				}
			}
			for _, c := range held {
				_ = c.Close()
			}
			for _, n := range []string{"a", "b", "c"} {
				require.NotZero(t, seen[n], "%s never connected to %s: %v", mech, n, seen)
			}
			if mech == "rr" {
				require.Equal(t, map[string]int{"a": 20, "b": 20, "c": 20}, seen)
			}
		})
	}
}

// with connections held open, least connections keeps the members level
func TestStreamLBLeastConnectionsHoldsLevel(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members, _ := tcpMembers(t, "a", "b", "c")
	s := startStreamLB(t, "tcp", members, "", "      mechanism: lc\n")
	held := make(map[string]int)
	var conns []net.Conn
	for range 30 {
		name, conn := s.ask(t)
		require.NotEmpty(t, name)
		held[name]++
		conns = append(conns, conn)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	require.Equal(t, map[string]int{"a": 10, "b": 10, "c": 10}, held)
}

// hrw keeps a client on one member: every connection here comes from 127.0.0.1
func TestStreamLBHRWKeepsAClientOnOneMember(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members, _ := tcpMembers(t, "a", "b", "c", "d")
	s := startStreamLB(t, "tcp", members, "", "      mechanism: hrw\n")
	first := s.askAndClose(t)
	require.NotEmpty(t, first)
	for range 20 {
		require.Equal(t, first, s.askAndClose(t), "one client address reached two members")
	}
}

// a udp session is committed to one member for life, and sessions are spread across the pool
func TestStreamLBUDPSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members := [][2]string{{"a", udpEchoBackend(t, "a")}, {"b", udpEchoBackend(t, "b")}}
	s := startStreamLB(t, "udp", members, "", "      mechanism: p2c\n")
	seen := make(map[string]int)
	for i := range 12 {
		conn, err := net.Dial("udp", s.addr)
		require.NoError(t, err)
		var owner string
		for j := range 3 {
			_, _ = conn.Write([]byte("ping"))
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 32)
			n, err := conn.Read(buf)
			require.NoError(t, err, "session %d datagram %d", i, j)
			name, _, _ := strings.Cut(string(buf[:n]), ":")
			if j > 0 {
				require.Equal(t, owner, name, "session %d moved between members", i)
			}
			owner = name
		}
		seen[owner]++
		_ = conn.Close()
	}
	require.NotZero(t, seen["a"], "%v", seen)
	require.NotZero(t, seen["b"], "%v", seen)
}

// a member that goes down is taken out by its connect probe, and returns when it is back
func TestStreamLBConnectProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members, echoes := tcpMembers(t, "a", "b")
	probe := "    healthcheck:\n      interval: 100ms\n      timeout: 500ms\n      failure_threshold: 1\n      recovery_threshold: 1\n"
	s := startStreamLB(t, "tcp", members, probe, "      mechanism: rr\n      healthy_floor: 1\n")
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/trickster/health", s.ports[1])
	requireHealthState(t, healthURL, "a", "available", 10*time.Second)
	requireHealthState(t, healthURL, "b", "available", 10*time.Second)

	echoes["a"].stop()
	requireHealthState(t, healthURL, "a", "unavailable", 10*time.Second)
	for range 10 {
		require.Equal(t, "b", s.askAndClose(t), "a connection was offered to the member that is down")
	}
	echoes["a"].restart(t)
	requireHealthState(t, healthURL, "a", "available", 10*time.Second)
	require.Eventually(t, func() bool { return s.askAndClose(t) == "a" }, 5*time.Second, 50*time.Millisecond,
		"the recovered member never took a connection")
}

// with no probe, a dead member refuses its share until passive health ejects it; with
// retries, its share is served by the others from the start
func TestStreamLBPassiveHealthAndRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	t.Run("default refuses the dead member's share", func(t *testing.T) {
		members, echoes := tcpMembers(t, "a", "b")
		s := startStreamLB(t, "tcp", members, "", "      mechanism: rr\n")
		echoes["a"].stop()
		var refused int
		for range 10 {
			if s.askAndClose(t) == "" {
				refused++
			}
		}
		require.Equal(t, 5, refused, "a dead member's share is refused, not shed")
	})
	t.Run("passive health ejects it", func(t *testing.T) {
		members, echoes := tcpMembers(t, "a", "b", "c")
		s := startStreamLB(t, "tcp", members, "",
			"      mechanism: rr\n      stream:\n        passive_health: {failures: 2, eject: 1h}\n")
		echoes["a"].stop()
		var refused int
		for range 30 {
			if s.askAndClose(t) == "" {
				refused++
			}
		}
		require.Equal(t, 2, refused, "the member was ejected after two failed connects, and refused no more")
	})
	t.Run("connect retries serve its share elsewhere", func(t *testing.T) {
		members, echoes := tcpMembers(t, "a", "b", "c")
		s := startStreamLB(t, "tcp", members, "", "      mechanism: rr\n      stream:\n        connect_retries: 1\n")
		echoes["a"].stop()
		before := echoes["b"].accepted.Load() + echoes["c"].accepted.Load()
		for range 15 {
			require.NotEmpty(t, s.askAndClose(t), "a connection was refused although retries are on")
		}
		require.EqualValues(t, 15, echoes["b"].accepted.Load()+echoes["c"].accepted.Load()-before)
	})
}

// a reload changes where new connections go and leaves the open ones where they are
func TestStreamLBReloadMidTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	signal.Reset(syscall.SIGHUP)
	members, _ := tcpMembers(t, "a", "b")
	s := startStreamLB(t, "tcp", members[:1], "", "      mechanism: rr\n")
	name, held := s.ask(t)
	require.Equal(t, "a", name)
	defer held.Close()

	s.write(t, members[1:], "", "      mechanism: lc\n")
	// the daemon reloads only a config whose file is newer than the one it loaded
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(s.cfgPath, future, future))
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	require.Eventually(t, func() bool { return s.askAndClose(t) == "b" }, 15*time.Second, 100*time.Millisecond,
		"new connections never reached the reloaded pool")

	_ = held.SetDeadline(time.Now().Add(5 * time.Second))
	_, err := held.Write([]byte("still\n"))
	require.NoError(t, err)
	reply, err := bufio.NewReader(held).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "a:still\n", reply, "a connection open across the reload moved or broke")
}

// a backup member takes connections only while no other member is available
func TestStreamLBBackupMember(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members, echoes := tcpMembers(t, "primary", "standby")
	probe := "    healthcheck:\n      interval: 100ms\n      timeout: 500ms\n      failure_threshold: 1\n      recovery_threshold: 1\n"
	s := startStreamLB(t, "tcp", members, probe, "      mechanism: rr\n      healthy_floor: 1\n", "standby")
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/trickster/health", s.ports[1])
	requireHealthState(t, healthURL, "primary", "available", 10*time.Second)
	requireHealthState(t, healthURL, "standby", "available", 10*time.Second)
	for range 10 {
		require.Equal(t, "primary", s.askAndClose(t), "the standby took a connection while the primary was up")
	}
	echoes["primary"].stop()
	requireHealthState(t, healthURL, "primary", "unavailable", 10*time.Second)
	for range 5 {
		require.Equal(t, "standby", s.askAndClose(t))
	}
	echoes["primary"].restart(t)
	requireHealthState(t, healthURL, "primary", "available", 10*time.Second)
	require.Eventually(t, func() bool { return s.askAndClose(t) == "primary" }, 5*time.Second, 50*time.Millisecond)
	for range 5 {
		require.Equal(t, "primary", s.askAndClose(t), "the standby kept taking connections after the primary returned")
	}
}

// a race connects to its members together, so a dead one costs a client nothing
func TestStreamLBConnectRace(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members, echoes := tcpMembers(t, "a", "b", "c")
	s := startStreamLB(t, "tcp", members, "", "      mechanism: race\n")
	for range 10 {
		require.NotEmpty(t, s.askAndClose(t))
	}
	echoes["a"].stop()
	echoes["b"].stop()
	for range 10 {
		require.Equal(t, "c", s.askAndClose(t), "the one live member did not win the race")
	}
}

// every member of a mirror receives every datagram, and only the first answers the client
func TestStreamLBUDPMirror(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	var mu sync.Mutex
	got := map[string][]string{}
	sink := func(name string) string {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = pc.Close() })
		go func() {
			buf := make([]byte, 1500)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				mu.Lock()
				got[name] = append(got[name], string(buf[:n]))
				mu.Unlock()
				_, _ = pc.WriteTo([]byte(name+":"+string(buf[:n])), from)
			}
		}()
		return pc.LocalAddr().String()
	}
	members := [][2]string{{"a", sink("a")}, {"b", sink("b")}, {"c", sink("c")}}
	s := startStreamLB(t, "udp", members, "", "      mechanism: mirror\n")
	conn, err := net.Dial("udp", s.addr)
	require.NoError(t, err)
	defer conn.Close()
	for _, msg := range []string{"one", "two", "three"} {
		_, _ = conn.Write([]byte(msg))
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 32)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		require.Equal(t, "a:"+msg, string(buf[:n]))
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got["a"]) == 3 && len(got["b"]) == 3 && len(got["c"]) == 3
	}, 5*time.Second, 50*time.Millisecond, "not every member received every datagram")
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, err = conn.Read(make([]byte, 32))
	require.Error(t, err, "a mirror's reply reached the client")
}
