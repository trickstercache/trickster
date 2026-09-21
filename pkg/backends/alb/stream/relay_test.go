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
package stream

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

// echoTCP answers each line with prefix + line
func echoTCP(t *testing.T, prefix string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					_, _ = conn.Write([]byte(prefix + strings.TrimSpace(line) + "\n"))
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func echoUDP(t *testing.T, prefix string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo([]byte(prefix+string(buf[:n])), from)
		}
	}()
	return pc.LocalAddr().String()
}

func tableOf(t *testing.T, u l4.Upstream) *l4.Table {
	t.Helper()
	tbl := l4.NewTable()
	if err := tbl.Add("", u); err != nil {
		t.Fatal(err)
	}
	return tbl
}

// each connection commits to one member: live members answer in turn, and the share of one
// that cannot be dialed is refused rather than handed to a sibling
func TestTCPRelayBalancesAcrossThePool(t *testing.T) {
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := deadLn.Addr().String()
	_ = deadLn.Close()
	pool := newALB(t, "alb", "rr",
		up(origin(t, "dead", dead), 1), up(origin(t, "a", echoTCP(t, "a:")), 1), up(origin(t, "b", echoTCP(t, "b:")), 1))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := l4.NewServer("test", l4.ProtocolTCP, &l4.Config{Table: tableOf(t, FromBackend(pool))})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	seen := make(map[string]int)
	var refused int
	for range 6 {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte("x\n"))
		reply, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err != nil {
			refused++
			continue
		}
		seen[strings.TrimSpace(reply)]++
	}
	if seen["a:x"] != 2 || seen["b:x"] != 2 || refused != 2 {
		t.Errorf("replies = %v, refused %d; want each member its share", seen, refused)
	}
}

// a session commits to one member for life; a member under the reserved .invalid domain
// holds its share of sessions and refuses them
func TestUDPRelayBalancesAcrossThePool(t *testing.T) {
	pool := newALB(t, "alb", "rr",
		up(origin(t, "a", echoUDP(t, "a:")), 1), up(origin(t, "b", echoUDP(t, "b:")), 1),
		up(origin(t, "gone", "unresolved.kgw.invalid:1"), 2))
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := l4.NewPacketServer("test", &l4.Config{Table: tableOf(t, FromBackend(pool))})
	go func() { _ = srv.Serve(pc) }()
	t.Cleanup(func() { _ = srv.Close() })

	seen := make(map[string]int)
	var refused int
	for i := range 8 {
		conn, err := net.Dial("udp", pc.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		var first string
		for j := range 2 {
			_, _ = conn.Write([]byte(strconv.Itoa(j)))
			_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			buf := make([]byte, 16)
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			member := string(buf[:2])
			if j == 0 {
				first = member
			} else if member != first {
				t.Errorf("session %d moved from %s to %s", i, first, member)
			}
			_ = n
		}
		_ = conn.Close()
		if first == "" {
			refused++
			continue
		}
		seen[first]++
	}
	if seen["a:"] != 2 || seen["b:"] != 2 || refused != 4 {
		t.Errorf("sessions = %v, refused %d; want 2, 2 and the refusing member's 4", seen, refused)
	}
}
