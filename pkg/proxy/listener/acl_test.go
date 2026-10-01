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

package listener

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	aclhandler "github.com/trickstercache/trickster/v2/pkg/proxy/ipacl/handler"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func compileACL(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	list, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func aclListenerOn(t *testing.T, address string, list *atomic.Pointer[ipacl.List], proxy *ProxyProtocolOptions) net.Listener {
	t.Helper()
	ln, err := NewListener(address, 0, 0, nil, proxy, list, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

type accepted struct {
	c   net.Conn
	err error
}

func acceptAsync(ln net.Listener) <-chan accepted {
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()
	return ch
}

// dialDenied dials a peer the listener should reset. The reset can win the race
// against the handshake, so a connect error of ECONNRESET is the denial itself.
func dialDenied(t *testing.T, ln net.Listener) {
	t.Helper()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		if errors.Is(err, syscall.ECONNRESET) {
			return
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	expectReset(t, c)
}

func dialListener(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func expectNoAccept(t *testing.T, ch <-chan accepted) {
	t.Helper()
	select {
	case got := <-ch:
		if got.c != nil {
			_ = got.c.Close()
		}
		t.Fatalf("Accept returned a denied connection: %v", got.err)
	default:
	}
}

func expectDrop(t *testing.T, client net.Conn) {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the client read %v, want a close", err)
	}
}

func takeAccepted(t *testing.T, ch <-chan accepted) net.Conn {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		t.Cleanup(func() { _ = got.c.Close() })
		return got.c
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was accepted")
	}
	return nil
}

func TestACLAllowsPermittedPeer(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}))
	ln := aclListenerOn(t, "127.0.0.1", &list, nil)
	acceptedCh := acceptAsync(ln)
	client := dialListener(t, ln)
	server := takeAccepted(t, acceptedCh)
	if server.RemoteAddr().String() != client.LocalAddr().String() {
		t.Fatalf("accepted %s, dialed from %s", server.RemoteAddr(), client.LocalAddr())
	}
}

func TestACLDropThenAllow(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{
		Allow: []string{"10.0.0.0/8"}, Source: "peer", Action: "drop",
	}))
	ln := aclListenerOn(t, "127.0.0.1", &list, nil)
	acceptedCh := acceptAsync(ln)

	denied := dialListener(t, ln)
	expectDrop(t, denied)
	expectNoAccept(t, acceptedCh)

	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer", Action: "drop"}))
	_ = dialListener(t, ln)
	takeAccepted(t, acceptedCh)
}

func TestACLRejectThenAllow(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"}))
	ln := aclListenerOn(t, "127.0.0.1", &list, nil)
	acceptedCh := acceptAsync(ln)

	dialDenied(t, ln)
	expectNoAccept(t, acceptedCh)

	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}))
	_ = dialListener(t, ln)
	takeAccepted(t, acceptedCh)
}

func TestACLIPV6(t *testing.T) {
	raw, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip(err)
	}
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"::1"}, Source: "peer"}))
	ln := newACLListener(raw, &list, nil, true)
	t.Cleanup(func() { _ = ln.Close() })

	allowed := acceptAsync(ln)
	client := dialListener(t, ln)
	server := takeAccepted(t, allowed)
	if server.RemoteAddr().String() != client.LocalAddr().String() {
		t.Fatalf("accepted %s, dialed from %s", server.RemoteAddr(), client.LocalAddr())
	}
	_ = server.Close()

	list.Store(compileACL(t, ipacl.Options{Allow: []string{"2001:db8::/32"}, Source: "peer"}))
	deniedCh := acceptAsync(ln)
	dialDenied(t, ln)
	expectNoAccept(t, deniedCh)
}

func TestACLClientIPMatchesPeerWithoutProxy(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "client_ip"}))
	ln := aclListenerOn(t, "127.0.0.1", &list, nil)
	acceptedCh := acceptAsync(ln)
	dialDenied(t, ln)
	expectNoAccept(t, acceptedCh)
}

func TestACLClientIPWithProxyIsNotJudgedAtAccept(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Source: "client_ip"}))
	ln, err := NewListener("127.0.0.1", 0, 0, nil, &ProxyProtocolOptions{Enabled: true}, &list, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	client, server := acceptOne(t, ln)
	if server.RemoteAddr() == nil || client.LocalAddr() == nil {
		t.Fatal("accepted connection has no address")
	}
}

func TestACLPeerDenyWithProxyProtocol(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"}))
	ln := aclListenerOn(t, "127.0.0.1", &list, &ProxyProtocolOptions{Enabled: true})
	acceptedCh := acceptAsync(ln)
	dialDenied(t, ln)
	expectNoAccept(t, acceptedCh)
}

func TestACLNilListAllows(t *testing.T) {
	ln := aclListenerOn(t, "127.0.0.1", nil, nil)
	for range 2 {
		client, server := acceptOne(t, ln)
		if server.RemoteAddr().String() != client.LocalAddr().String() {
			t.Fatalf("accepted %s, dialed from %s", server.RemoteAddr(), client.LocalAddr())
		}
	}
}

type scriptedListener struct {
	conns []net.Conn
}

func (s *scriptedListener) Accept() (net.Conn, error) {
	if len(s.conns) == 0 {
		return nil, net.ErrClosed
	}
	c := s.conns[0]
	s.conns = s.conns[1:]
	return c, nil
}

func (s *scriptedListener) Close() error   { return nil }
func (s *scriptedListener) Addr() net.Addr { return literalAddr("127.0.0.1:0") }

type literalAddr string

func (a literalAddr) Network() string { return "tcp" }
func (a literalAddr) String() string  { return string(a) }

type addrConn struct {
	net.Conn
	addr net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.addr }

func TestACLInvalidAddressIsDenied(t *testing.T) {
	nilFar, nilNear := net.Pipe()
	badFar, badNear := net.Pipe()
	emptyFar, emptyNear := net.Pipe()
	goodFar, goodNear := net.Pipe()
	t.Cleanup(func() {
		_ = nilFar.Close()
		_ = badFar.Close()
		_ = emptyFar.Close()
		_ = goodFar.Close()
		_ = nilNear.Close()
		_ = badNear.Close()
		_ = emptyNear.Close()
		_ = goodNear.Close()
	})
	raw := &scriptedListener{conns: []net.Conn{
		addrConn{Conn: nilNear, addr: nil},
		addrConn{Conn: badNear, addr: literalAddr("not-an-address")},
		addrConn{Conn: emptyNear, addr: &net.TCPAddr{}},
		addrConn{Conn: goodNear, addr: literalAddr("127.0.0.1:9")},
	}}
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}))
	ln := newACLListener(raw, &list, nil, true)

	got, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteAddr().String() != "127.0.0.1:9" {
		t.Fatalf("Accept returned %v", got.RemoteAddr())
	}
	for _, far := range []net.Conn{nilFar, badFar, emptyFar} {
		_ = far.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := far.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Errorf("denied pipe read %v, want EOF", err)
		}
	}
}

func TestACLDenialDoesNotHoldConnectionLimit(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"}))
	ln, err := NewListener("127.0.0.1", 0, 1, nil, nil, &list, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	first := acceptAsync(ln)
	dialDenied(t, ln)
	expectNoAccept(t, first)

	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}))
	_ = dialListener(t, ln)
	held := takeAccepted(t, first)

	second := acceptAsync(ln)
	_ = dialListener(t, ln)
	select {
	case got := <-second:
		if got.c != nil {
			_ = got.c.Close()
		}
		t.Fatal("a second connection was accepted while the limit was held")
	case <-time.After(200 * time.Millisecond):
	}
	_ = held.Close()
	takeAccepted(t, second)
}

func TestACLListenerOrder(t *testing.T) {
	plain, err := NewListener("127.0.0.1", 0, 0, nil, nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if got, want := listenerChain(plain), []string{"*listener.aclListener", "*net.TCPListener"}; !slices.Equal(got, want) {
		t.Fatalf("listener chain = %v, want %v", got, want)
	}

	full, err := NewListener("127.0.0.1", 0, 1, &tls.Config{MinVersion: tls.VersionTLS12},
		&ProxyProtocolOptions{Enabled: true}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = full.Close() })
	want := []string{
		"*tls.listener",
		"*proxyproto.Listener",
		"*netutil.limitListener",
		"*listener.aclListener",
		"*net.TCPListener",
	}
	if got := listenerChain(full); !slices.Equal(got, want) {
		t.Fatalf("listener chain = %v, want %v", got, want)
	}
}

func listenerChain(ln net.Listener) []string {
	var out []string
	for ln != nil {
		out = append(out, fmt.Sprintf("%T", ln))
		ln = unwrapListener(ln)
	}
	return out
}

func unwrapListener(ln net.Listener) net.Listener {
	v := reflect.ValueOf(ln)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil
	}
	field := v.Elem().FieldByName("Listener")
	if !field.IsValid() || !field.CanInterface() {
		return nil
	}
	next, _ := field.Interface().(net.Listener)
	return next
}

type recordingProtocolServer struct {
	accepted chan net.Conn
	mu       sync.Mutex
	ln       net.Listener
}

func (s *recordingProtocolServer) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		s.accepted <- c
	}
}

func (s *recordingProtocolServer) Shutdown(context.Context) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return nil
	}
	return ln.Close()
}

func TestACLNativeListener(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	lg := NewGroup()
	t.Cleanup(func() { _ = lg.Shutdown(time.Second) })

	svr := &recordingProtocolServer{accepted: make(chan net.Conn, 1)}
	deny := compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"})
	lg.SetIPACL("native", deny, "")
	errCh := make(chan error, 1)
	go func() {
		errCh <- lg.StartProtocolListener("native", "mysql", "127.0.0.1", 0, 0, svr, nil, nil)
	}()

	var ln *Listener
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ln = lg.Get("native"); ln != nil && ln.State() == StateReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ln == nil || ln.State() != StateReady {
		t.Fatal("native listener did not become ready")
	}

	dialDenied(t, ln)
	select {
	case c := <-svr.accepted:
		_ = c.Close()
		t.Fatal("the protocol server received the denied connection")
	default:
	}

	lg.SetIPACL("native", compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}), "")
	allowed, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = allowed.Close() })
	select {
	case c := <-svr.accepted:
		_ = c.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("the protocol server did not receive the allowed connection")
	}
	select {
	case err := <-errCh:
		t.Fatalf("listener stopped: %v", err)
	default:
	}
}

func TestAcceptJudgesClientIP(t *testing.T) {
	if !acceptJudgesClientIP("mysql", nil) || !acceptJudgesClientIP("postgres", nil) {
		t.Fatal("a native listener without PROXY protocol judges client_ip at accept")
	}
	if acceptJudgesClientIP("mysql", &ProxyProtocolOptions{Enabled: true}) {
		t.Fatal("PROXY protocol replaces the address a client_ip list needs")
	}
	if acceptJudgesClientIP("tcp", nil) || acceptJudgesClientIP("tls", nil) {
		t.Fatal("stream client_ip is resolved from the flow, not the socket")
	}
}

func readyListener(t *testing.T, lg *Group, name string) *Listener {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ln := lg.Get(name); ln != nil && ln.State() == StateReady {
			return ln
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("listener did not become ready")
	return nil
}

func httpExchange(t *testing.T, addr, request string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
}

func TestHTTPPeerDenyResetsBeforeProxyHeader(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	list := compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"})
	lg := NewGroup()
	t.Cleanup(func() { _ = lg.Shutdown(time.Second) })
	const name = "http-peer-deny"
	lg.SetIPACL(name, list, "")
	go func() {
		_ = lg.StartListener(name, "127.0.0.1", 0, 0, nil, aclhandler.Middleware(list, "office", aclhandler.ScopeListener, okHandler()),
			nil, nil, time.Second, &ProxyProtocolOptions{Enabled: true})
	}()
	ln := readyListener(t, lg, name)
	dialDenied(t, ln)
}

func TestHTTPPeerProxyDoesNotRejudge(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	list := compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"})
	lg := NewGroup()
	t.Cleanup(func() { _ = lg.Shutdown(time.Second) })
	const name = "http-peer-proxy"
	lg.SetIPACL(name, list, "")
	go func() {
		_ = lg.StartListener(name, "127.0.0.1", 0, 0, nil, aclhandler.Middleware(list, "office", aclhandler.ScopeListener, okHandler()),
			nil, nil, time.Second, &ProxyProtocolOptions{Enabled: true})
	}()
	ln := readyListener(t, lg, name)
	const request = "PROXY TCP4 192.0.2.9 10.0.0.1 4242 80\r\n" +
		"GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n"
	code, body := httpExchange(t, ln.Addr().String(), request)
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("allowed socket peer = %d %q", code, body)
	}
}

func TestHTTPClientIPUsesForwardedClient(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	list := compileACL(t, ipacl.Options{Allow: []string{"192.0.2.9"}, Source: "client_ip"})
	trusted, err := clientip.ParseTrusted([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	lg := NewGroup()
	t.Cleanup(func() { _ = lg.Shutdown(time.Second) })
	const name = "http-client-ip"
	lg.SetIPACL(name, list, "")
	handler := clientip.Middleware(trusted, aclhandler.Middleware(list, "office", aclhandler.ScopeListener, okHandler()))
	go func() {
		_ = lg.StartListener(name, "127.0.0.1", 0, 0, nil, handler, nil, nil, time.Second, nil)
	}()
	ln := readyListener(t, lg, name)
	request := func(forwarded string) string {
		return "GET / HTTP/1.1\r\nHost: example.com\r\nX-Forwarded-For: " + forwarded + "\r\nConnection: close\r\n\r\n"
	}
	code, body := httpExchange(t, ln.Addr().String(), request("192.0.2.9"))
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("forwarded client = %d %q", code, body)
	}
	code, body = httpExchange(t, ln.Addr().String(), request("198.51.100.8"))
	if code != http.StatusForbidden || strings.Contains(body, "ok") {
		t.Fatalf("other forwarded client = %d %q", code, body)
	}
}

func TestAcceptCountsAllowAndDeny(t *testing.T) {
	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"}))
	var dec atomic.Pointer[acceptACL]
	dec.Store(&acceptACL{
		dec:  metrics.NewIPACLDecision("accept-office", metrics.IPACLScopeListener),
		name: "accept-office",
	})
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newACLListener(inner, &list, &dec, true)
	t.Cleanup(func() { _ = ln.Close() })
	accepted := acceptAsync(ln)

	beforeDeny := testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(
		"accept-office", metrics.IPACLScopeListener, "deny"))
	dialDenied(t, ln)
	expectNoAccept(t, accepted)
	if got := testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(
		"accept-office", metrics.IPACLScopeListener, "deny")); got != beforeDeny+1 {
		t.Fatalf("deny = %v, want %v", got, beforeDeny+1)
	}

	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}))
	beforeAllow := testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(
		"accept-office", metrics.IPACLScopeListener, "allow"))
	_ = dialListener(t, ln)
	takeAccepted(t, accepted)
	if got := testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(
		"accept-office", metrics.IPACLScopeListener, "allow")); got != beforeAllow+1 {
		t.Fatalf("allow = %v, want %v", got, beforeAllow+1)
	}
}

// lockedBuffer lets the accept goroutine write a log line while the test reads it.
// The TCP reset that dialDenied waits on is not a race-detector edge.
type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Len()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Buffer.Reset()
}

func TestAcceptDenialLog(t *testing.T) {
	buf := &lockedBuffer{}
	lg := logging.StreamLogger(buf, level.Debug)
	lg.SetLogAsynchronous(false)
	logger.SetLogger(lg)
	t.Cleanup(func() { logger.SetLogger(logging.NoopLogger()) })

	var list atomic.Pointer[ipacl.List]
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"}))
	var dec atomic.Pointer[acceptACL]
	dec.Store(&acceptACL{
		dec:  metrics.NewIPACLDecision("accept-log", metrics.IPACLScopeListener),
		name: "accept-log",
	})
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newACLListener(inner, &list, &dec, true)
	t.Cleanup(func() { _ = ln.Close() })
	accepted := acceptAsync(ln)
	dialDenied(t, ln)
	expectNoAccept(t, accepted)
	line := buf.String()
	for _, want := range []string{"level=debug", "ip_acl=accept-log", "scope=listener", "address=127.0.0.1", "action=reject"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q missing %q", line, want)
		}
	}

	buf.Reset()
	logger.SetLogLevel(level.Info)
	dialDenied(t, ln)
	expectNoAccept(t, accepted)
	if buf.Len() != 0 {
		t.Fatalf("info logged %q", buf.String())
	}

	buf.Reset()
	logger.SetLogLevel(level.Debug)
	list.Store(compileACL(t, ipacl.Options{Allow: []string{"127.0.0.1"}, Source: "peer"}))
	_ = dialListener(t, ln)
	takeAccepted(t, accepted)
	if buf.Len() != 0 {
		t.Fatalf("allow logged %q", buf.String())
	}
}
