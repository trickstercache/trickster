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

package quicmux

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
)

const (
	loopback    = "127.0.0.1"
	alpnA       = "proto-a"
	alpnB       = "proto-b"
	hostA       = "a.example.com"
	hostB       = "b.example.com"
	testTimeout = 5 * time.Second
)

type fakeHandler struct {
	conns     chan *quic.Conn
	shutdowns atomic.Int32
	mu        sync.Mutex
	open      []*quic.Conn
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{conns: make(chan *quic.Conn, 16)}
}

func (f *fakeHandler) ServeQUIC(c *quic.Conn) {
	f.mu.Lock()
	f.open = append(f.open, c)
	f.mu.Unlock()
	f.conns <- c
	<-c.Context().Done()
}

func (f *fakeHandler) Shutdown(context.Context) error {
	f.shutdowns.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.open {
		_ = c.CloseWithError(0, "")
	}
	return nil
}

func certSwapper(t *testing.T, names ...string) sw.CertSwapper {
	t.Helper()
	key, cert, err := testtls.GetTestKeyAndCertWithNames(names...)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return sw.NewSwapper([]tls.Certificate{pair})
}

func member(t *testing.T, h Handler, host string, alpns ...string) MemberOptions {
	t.Helper()
	return MemberOptions{
		Name: alpns[0], ALPNs: alpns, Handler: h, Certs: certSwapper(t, host),
		TLS: &tls.Config{MinVersion: tls.VersionTLS13}, QUIC: &quic.Config{EnableDatagrams: true},
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", loopback+":0")
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

func dial(t *testing.T, addr net.Addr, sni string, alpns ...string) (*quic.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	return quic.DialAddr(ctx, addr.String(), &tls.Config{
		ServerName: sni, NextProtos: alpns, MinVersion: tls.VersionTLS13,
		InsecureSkipVerify: true, // #nosec G402 -- test client against self-signed test certs
	}, &quic.Config{})
}

func awaitConn(t *testing.T, h *fakeHandler) *quic.Conn {
	t.Helper()
	select {
	case c := <-h.conns:
		return c
	case <-time.After(testTimeout):
		t.Fatal("handler was not handed the connection")
		return nil
	}
}

func shutdown(t *testing.T, m *Member) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Error(err)
	}
}

func TestJoinValidates(t *testing.T) {
	h := newFakeHandler()
	certs := certSwapper(t, hostA)
	tests := []struct {
		name string
		o    MemberOptions
		want error
	}{
		{"no alpn", MemberOptions{Handler: h, Certs: certs}, ErrNoALPN},
		{"no handler", MemberOptions{ALPNs: []string{alpnA}, Certs: certs}, ErrNoHandler},
		{"no certs", MemberOptions{ALPNs: []string{alpnA}, Handler: h}, ErrNoCerts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Join(loopback, 0, tc.o); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := Join("240.0.0.1", 1, member(t, h, hostA, alpnA)); err == nil {
		t.Error("expected an error binding an unusable address")
	}
}

func TestSharedEndpointDispatchesByALPN(t *testing.T) {
	port := freeUDPPort(t)
	ha, hb := newFakeHandler(), newFakeHandler()
	ma, err := Join(loopback, port, member(t, ha, hostA, alpnA))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := Join(loopback, port, member(t, hb, hostB, alpnB))
	if err != nil {
		t.Fatal(err)
	}
	if ma.ep != mb.ep {
		t.Fatal("members at one address should share an endpoint")
	}
	if _, err := Join(loopback, port, member(t, newFakeHandler(), hostA, alpnA)); !errors.Is(err, ErrALPNInUse) {
		t.Errorf("a second member serving %s should be refused, got %v", alpnA, err)
	}

	for _, tc := range []struct {
		alpn string
		h    *fakeHandler
	}{{alpnA, ha}, {alpnB, hb}} {
		c, err := dial(t, ma.LocalAddr(), hostA, tc.alpn)
		if err != nil {
			t.Fatalf("dial %s: %v", tc.alpn, err)
		}
		served := awaitConn(t, tc.h)
		if got := served.ConnectionState().TLS.NegotiatedProtocol; got != tc.alpn {
			t.Errorf("handler for %s was handed a %s connection", tc.alpn, got)
		}
		_ = c.CloseWithError(0, "")
	}
	if c, err := dial(t, ma.LocalAddr(), hostA, "proto-c"); err == nil {
		_ = c.CloseWithError(0, "")
		t.Error("a protocol no member serves should fail the handshake")
	}

	if ma.Handler() != Handler(ha) {
		t.Error("a member should expose its handler")
	}
	maServed := make(chan error, 1)
	go func() { maServed <- ma.Serve() }()
	shutdown(t, ma)
	if ha.shutdowns.Load() != 1 {
		t.Error("a leaving member's handler should be shut down")
	}
	select {
	case err := <-maServed:
		if err != nil {
			t.Errorf("a member that left should serve no error, got %v", err)
		}
	case <-time.After(testTimeout):
		t.Error("Serve did not return when its member left")
	}
	if c, err := dial(t, mb.LocalAddr(), hostB, alpnA); err == nil {
		_ = c.CloseWithError(0, "")
		t.Error("a protocol whose member left should fail the handshake")
	}
	c, err := dial(t, mb.LocalAddr(), hostB, alpnB)
	if err != nil {
		t.Fatalf("the remaining member stopped serving: %v", err)
	}
	awaitConn(t, hb)
	_ = c.CloseWithError(0, "")
	if probe, err := net.ListenPacket("udp", mb.LocalAddr().String()); err == nil {
		_ = probe.Close()
		t.Error("the socket should stay bound while a member remains")
	}

	shutdown(t, mb)
	probe, err := net.ListenPacket("udp", mb.LocalAddr().String())
	if err != nil {
		t.Fatalf("the last member leaving should release the socket: %v", err)
	}
	_ = probe.Close()
	if err := mb.Serve(); err != nil {
		t.Errorf("a member that left should serve no error, got %v", err)
	}
	endpoints.mu.Lock()
	_, registered := endpoints.endpoints[endpointKey(loopback, port)]
	endpoints.mu.Unlock()
	if registered {
		t.Error("a closed endpoint should be unregistered")
	}
}

func TestEphemeralPortsAreNotShared(t *testing.T) {
	ma, err := Join(loopback, 0, member(t, newFakeHandler(), hostA, alpnA))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ma.Close() }()
	mb, err := Join(loopback, 0, member(t, newFakeHandler(), hostA, alpnA))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mb.Close() }()
	if ma.ep == mb.ep {
		t.Error("two listeners on port 0 should each bind their own socket")
	}
}

func TestCertificateSelection(t *testing.T) {
	port := freeUDPPort(t)
	ha, hb := newFakeHandler(), newFakeHandler()
	ma, err := Join(loopback, port, member(t, ha, hostA, alpnA))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ma.Close() }()

	leaf := func(c *quic.Conn) string {
		return c.ConnectionState().TLS.PeerCertificates[0].DNSNames[0]
	}
	// a lone member serves its certificate whatever the client asks for
	c, err := dial(t, ma.LocalAddr(), "other.example.com", alpnA)
	if err != nil {
		t.Fatal(err)
	}
	if got := leaf(c); got != hostA {
		t.Errorf("lone member served %s", got)
	}
	_ = c.CloseWithError(0, "")
	awaitConn(t, ha)

	mb, err := Join(loopback, port, member(t, hb, hostB, alpnB))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mb.Close() }()
	tests := []struct {
		name, sni, alpn, want string
		h                     *fakeHandler
	}{
		{"host names the other member's certificate", hostB, alpnA, hostB, ha},
		{"host names the protocol's member", hostA, alpnA, hostA, ha},
		{"unknown host falls back to the protocol's member", "other.example.com", alpnB, hostB, hb},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := dial(t, ma.LocalAddr(), tc.sni, tc.alpn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.CloseWithError(0, "") }()
			if got := leaf(c); got != tc.want {
				t.Errorf("served %s, want %s", got, tc.want)
			}
			awaitConn(t, tc.h)
		})
	}
}

func TestGetCertificateFallbacks(t *testing.T) {
	e := &endpoint{}
	e.publish()
	if _, err := e.getCertificate(&tls.ClientHelloInfo{}); !errors.Is(err, sw.ErrNoCertificates) {
		t.Errorf("an endpoint with no members has no certificate, got %v", err)
	}
	ma := &Member{opts: member(t, newFakeHandler(), hostA, alpnA)}
	mb := &Member{opts: member(t, newFakeHandler(), hostB, alpnB)}
	e.members = []*Member{ma, mb}
	e.publish()
	// a client offering no served protocol and naming no known host gets the first member's certificate
	c, err := e.getCertificate(&tls.ClientHelloInfo{ServerName: "other.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.Leaf.DNSNames, hostA) {
		t.Errorf("expected the first member's certificate, got %v", c.Leaf.DNSNames)
	}
}

func TestMergeQUIC(t *testing.T) {
	m := func(q *quic.Config) *Member { return &Member{opts: MemberOptions{QUIC: q}} }
	out := mergeQUIC([]*Member{
		m(&quic.Config{
			MaxIdleTimeout: time.Minute, MaxIncomingStreams: 100, EnableDatagrams: true,
			KeepAlivePeriod: 20 * time.Second, Allow0RTT: true, MaxStreamReceiveWindow: 1 << 20,
		}),
		m(nil),
		m(&quic.Config{
			MaxIdleTimeout: 2 * time.Minute, MaxIncomingUniStreams: 1000, KeepAlivePeriod: 10 * time.Second,
			EnableStreamResetPartialDelivery: true, HandshakeIdleTimeout: 3 * time.Second,
			InitialStreamReceiveWindow: 1 << 16, InitialConnectionReceiveWindow: 1 << 17,
			MaxConnectionReceiveWindow: 1 << 22,
		}),
		m(&quic.Config{MaxIncomingStreams: -1}),
	})
	switch {
	case out.MaxIdleTimeout != 2*time.Minute:
		t.Errorf("idle timeout %v", out.MaxIdleTimeout)
	case out.MaxIncomingStreams != 100 || out.MaxIncomingUniStreams != 1000:
		t.Errorf("streams %d/%d", out.MaxIncomingStreams, out.MaxIncomingUniStreams)
	case out.KeepAlivePeriod != 10*time.Second:
		t.Errorf("keep-alive %v", out.KeepAlivePeriod)
	case !out.EnableDatagrams || !out.EnableStreamResetPartialDelivery:
		t.Error("a feature one member needs should be enabled")
	case out.Allow0RTT:
		t.Error("0-RTT should never be accepted")
	case out.HandshakeIdleTimeout != 3*time.Second || out.MaxStreamReceiveWindow != 1<<20 ||
		out.InitialStreamReceiveWindow != 1<<16 || out.InitialConnectionReceiveWindow != 1<<17 ||
		out.MaxConnectionReceiveWindow != 1<<22:
		t.Errorf("windows and handshake timeout not widened: %+v", out)
	}
}

type failingConn struct {
	net.PacketConn
	fail atomic.Bool
}

var errSocket = errors.New("socket failed")

func (f *failingConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if f.fail.Load() {
		return 0, nil, errSocket
	}
	n, addr, err := f.PacketConn.ReadFrom(b)
	if f.fail.Load() {
		return 0, nil, errSocket
	}
	return n, addr, err
}

func TestServeReportsEndpointFailure(t *testing.T) {
	inner, err := net.ListenPacket("udp", loopback+":0")
	if err != nil {
		t.Fatal(err)
	}
	fc := &failingConn{PacketConn: inner}
	m, err := endpoints.join(loopback, 0, member(t, newFakeHandler(), hostA, alpnA),
		func() (net.PacketConn, error) { return fc, nil })
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- m.Serve() }()
	fc.fail.Store(true)
	// a datagram wakes the blocked read so the failure is noticed
	if c, err := net.Dial("udp", inner.LocalAddr().String()); err == nil {
		_, _ = c.Write([]byte{0})
		_ = c.Close()
	}
	select {
	case err := <-served:
		if err == nil {
			t.Error("a member should report the endpoint's failure")
		}
	case <-time.After(testTimeout):
		t.Fatal("Serve did not return when the endpoint failed")
	}
	_ = m.Close()
}

func TestJoinLeaveConcurrently(t *testing.T) {
	port := freeUDPPort(t)
	anchor, err := Join(loopback, port, member(t, newFakeHandler(), hostA, alpnA))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = anchor.Close() }()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			m, err := Join(loopback, port, member(t, newFakeHandler(), hostB, alpnB+string(rune('a'+i))))
			if err != nil {
				t.Error(err)
				return
			}
			_ = m.Close()
		})
	}
	wg.Wait()
	if n := len(anchor.ep.snap.Load().members); n != 1 {
		t.Errorf("expected only the anchor to remain, found %d members", n)
	}
}

func TestDispatchWithoutMemberClosesConnection(t *testing.T) {
	h := newFakeHandler()
	m, err := Join(loopback, 0, member(t, h, hostA, alpnA))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	// the handshake still offers the protocol, but its member is gone by the time of dispatch
	s := *m.ep.snap.Load()
	s.byALPN = map[string]*Member{}
	m.ep.snap.Store(&s)
	// the close may arrive while the client is still handshaking, failing the dial, or just after it
	c, err := dial(t, m.LocalAddr(), hostA, alpnA)
	if err == nil {
		select {
		case <-c.Context().Done():
			err = context.Cause(c.Context())
		case <-time.After(testTimeout):
			t.Fatal("a connection without a member should be closed")
		}
	}
	// before the handshake is confirmed, an application close travels as the APPLICATION_ERROR
	// transport error with no reason phrase (RFC 9000 10.2.3)
	var appErr *quic.ApplicationError
	var trErr *quic.TransportError
	if !(errors.As(err, &appErr) && appErr.ErrorCode == codeNoProtocol) &&
		!(errors.As(err, &trErr) && trErr.ErrorCode == quic.ApplicationErrorErrorCode) {
		t.Errorf("unexpected close cause %v", err)
	}
	select {
	case <-h.conns:
		t.Error("no handler should be handed the connection")
	default:
	}
}
