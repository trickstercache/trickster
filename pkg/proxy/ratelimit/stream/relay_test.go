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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

func TestRelayRejectsCloseAndPassesUnderLimit(t *testing.T) {
	echo := lineEcho(t, "echo")
	start := func(adm l4.Admission) string {
		t.Helper()
		return serveTCP(t, l4.ProtocolTCP, "", echo, adm)
	}
	addr := start(New(opts(t, "relay-reject", nil), l4.ProtocolTCP, false))
	if got := roundTrip(t, addr, "one"); got != "echo:one" {
		t.Fatalf("reply %q", got)
	}
	expectReset(t, dial(t, addr))

	addr = start(New(opts(t, "relay-close", func(o *options.Options) {
		o.Action = options.ActionClose
	}), l4.ProtocolTCP, false))
	roundTrip(t, addr, "open")
	expectClose(t, dial(t, addr))

	addr = start(New(opts(t, "relay-under", func(o *options.Options) { o.Limit = 2 }), l4.ProtocolTCP, false))
	if roundTrip(t, addr, "a") != "echo:a" || roundTrip(t, addr, "b") != "echo:b" {
		t.Fatal("an under-limit connection was refused")
	}
}

func TestRelaySNIAfterTheHandshake(t *testing.T) {
	cert := testCert(t, "shop.example.com")
	echo := tlsEcho(t, cert)
	adm := New(opts(t, "relay-sni", func(o *options.Options) { o.Keys = []string{"sni"} }), l4.ProtocolTLS, false)
	addr := serveTCP(t, l4.ProtocolTLS, "shop.example.com", echo, adm)
	if err := handshake(t, addr, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := handshake(t, addr, "shop.example.com"); err == nil {
		t.Fatal("the second connection for one name was admitted")
	}
	if err := handshake(t, addr, "other.example.com"); err == nil {
		t.Fatal("an unroutable name completed its handshake")
	}
	if buckets("relay-sni") != 1 {
		t.Fatalf("unroutable tls stored %d buckets", buckets("relay-sni"))
	}
}

func TestRelaySessionsDatagramsAndSwap(t *testing.T) {
	echo := packetEcho(t, "echo")
	sessions := New(opts(t, "relay-sess", func(o *options.Options) {
		o.Unit = options.UnitSessions
	}), l4.ProtocolUDP, false)
	if _, ok := sessions.(l4.Holder); !ok {
		t.Fatal("sessions do not hold")
	}
	srv, addr := serveUDP(t, echo, sessions)
	if got := packet(t, addr, "one"); got != "echo:one" {
		t.Fatalf("first session %q", got)
	}
	held, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.Write([]byte("two")); err != nil {
		t.Fatal(err)
	}
	_ = held.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _ := held.Read(make([]byte, 32)); n != 0 {
		t.Fatal("a second session was relayed during the hold")
	}
	open := New(opts(t, "relay-sess-open", func(o *options.Options) {
		o.Unit = options.UnitSessions
		o.Limit = 10
	}), l4.ProtocolUDP, false)
	srv.Update(&l4.Config{Table: table(echo), Admission: open})
	if _, err := held.Write([]byte("three")); err != nil {
		t.Fatal(err)
	}
	_ = held.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 32)
	n, err := held.Read(buf)
	if err != nil || string(buf[:n]) != "echo:three" {
		t.Fatalf("the swap did not release the hold: %q %v", buf[:n], err)
	}
	if buckets("relay-sess") != 1 {
		t.Fatalf("the swap dropped the counter, buckets %d", buckets("relay-sess"))
	}

	grams := New(opts(t, "relay-grams", func(o *options.Options) {
		o.Unit = options.UnitDatagrams
	}), l4.ProtocolUDP, false)
	if _, ok := grams.(l4.Holder); ok || !grams.Datagrams() {
		t.Fatal("datagrams hold the client or skip datagrams")
	}
	_, addr = serveUDP(t, echo, grams)
	if packet(t, addr, "a") != "echo:a" {
		t.Fatal("first datagram")
	}
	if packetTimed(t, addr, "b", 200*time.Millisecond) != "" {
		t.Fatal("the datagram past the limit was relayed")
	}
}

func TestHoldShorterAndLongerThanTheDefault(t *testing.T) {
	short := New(opts(t, "hold-short", func(o *options.Options) {
		o.Unit = options.UnitSessions
		o.Window = timeconv.Duration(time.Second)
	}), l4.ProtocolUDP, false)
	short.Peer(l4.Flow{})
	short.Peer(l4.Flow{})
	if got := short.(l4.Holder).Hold(l4.Flow{}); got <= 0 || got >= l4.DefaultDeniedHold {
		t.Fatalf("short hold %v", got)
	}
	long := New(opts(t, "hold-long", func(o *options.Options) {
		o.Unit = options.UnitSessions
		o.Window = timeconv.Duration(time.Hour)
	}), l4.ProtocolUDP, false)
	long.Peer(l4.Flow{})
	long.Peer(l4.Flow{})
	if got := long.(l4.Holder).Hold(l4.Flow{}); got <= l4.DefaultDeniedHold {
		t.Fatalf("long hold %v", got)
	}
}

func lineEcho(t *testing.T, name string) string {
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
					_, _ = conn.Write([]byte(name + ":" + strings.TrimSpace(line) + "\n"))
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func tlsEcho(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
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
				buf := make([]byte, 8)
				_, _ = conn.Read(buf)
			}()
		}
	}()
	return ln.Addr().String()
}

func serveTCP(t *testing.T, protocol, host, upstream string, adm l4.Admission) string {
	t.Helper()
	tbl := l4.NewTable()
	if err := tbl.Add(host, l4.Static(upstream)); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := l4.NewServer("relay", protocol, &l4.Config{Table: tbl, Admission: adm})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func serveUDP(t *testing.T, upstream string, adm l4.Admission) (*l4.PacketServer, string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := l4.NewPacketServer("relay", &l4.Config{Table: table(upstream), Admission: adm})
	go func() { _ = srv.Serve(pc) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, pc.LocalAddr().String()
}

func table(upstream string) *l4.Table {
	tbl := l4.NewTable()
	_ = tbl.Add("", l4.Static(upstream))
	return tbl
}

func packetEcho(t *testing.T, name string) string {
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
			_, _ = pc.WriteTo([]byte(name+":"+string(buf[:n])), from)
		}
	}()
	return pc.LocalAddr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func roundTrip(t *testing.T, addr, line string) string {
	t.Helper()
	conn := dial(t, addr)
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(got)
}

func expectReset(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || !isReset(err) {
		t.Fatalf("read %v, want a reset", err)
	}
}

func expectClose(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err == nil || isReset(err) || n != 0 {
		t.Fatalf("read %q %v, want a close", buf[:n], err)
	}
}

func isReset(err error) bool {
	return err != nil && (strings.Contains(err.Error(), syscall.ECONNRESET.Error()) || strings.Contains(err.Error(), "connection reset"))
}

func packet(t *testing.T, addr, msg string) string {
	t.Helper()
	return packetTimed(t, addr, msg, 3*time.Second)
}

func packetTimed(t *testing.T, addr, msg string, wait time.Duration) string {
	t.Helper()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return ""
	}
	return string(buf[:n])
}

func handshake(t *testing.T, addr, name string) error {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return tls.Client(conn, &tls.Config{ServerName: name, InsecureSkipVerify: true}).Handshake() // #nosec G402 -- a test certificate
}

func testCert(t *testing.T, host string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
