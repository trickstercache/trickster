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

package http3

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/quicmux"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

const (
	testListener = "web"
	testProtocol = "moqt-21"
	testPing     = "ping"
	// outlasts the 1ms request deadline the test endpoint's limits put on a request body
	pastDeadline = 20 * time.Millisecond
)

type wtEndpoint struct {
	h      *Handler
	member *quicmux.Member
	addr   string
	pool   *x509.CertPool
}

func startWTEndpoint(t *testing.T, handler http.Handler) *wtEndpoint {
	t.Helper()
	key, cert, err := testtls.GetTestKeyAndCertWithNames(testHost)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(cert)
	h := NewHandler(handler, listener.ServerLimits{ReadHeaderTimeout: time.Millisecond})
	h.SetWebTransport(true, testListener)
	m, err := quicmux.Join(testLoopback, 0, quicmux.MemberOptions{
		Name: "wttest", ALPNs: []string{ALPN}, Handler: h, Certs: sw.NewSwapper([]tls.Certificate{pair}),
		TLS: &tls.Config{MinVersion: tls.VersionTLS13}, QUIC: QUICConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = m.Serve() }()
	t.Cleanup(func() { _ = m.Close() })
	return &wtEndpoint{h: h, member: m, addr: m.LocalAddr().String(), pool: pool}
}

func (e *wtEndpoint) tlsConfig() *tls.Config {
	return &tls.Config{RootCAs: e.pool, ServerName: testHost, MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN}}
}

func (e *wtEndpoint) dial(t *testing.T, protocols ...string) (*http.Response, *webtransport.Session, error) {
	t.Helper()
	tr := &webtransport.Transport{TLSClientConfig: e.tlsConfig(), ApplicationProtocols: protocols}
	ctx, cancel := context.WithTimeout(context.Background(), testDialTimeout)
	defer cancel()
	return tr.Dial(ctx, "https://"+e.addr+"/moq", nil)
}

func echoSessions(t *testing.T, seen chan<- string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := WebTransportRequest(r)
		if !ok {
			if _, err := UpgradeWebTransport(r, testProtocol); !errors.Is(err, ErrNotWebTransport) {
				t.Errorf("a plain request opens no session: %v", err)
			}
			_, _ = io.WriteString(w, "plain")
			return
		}
		seen <- name
		protocol := ""
		if offered := r.Header.Get(HeaderWTAvailableProtocols); strings.Contains(offered, testProtocol) {
			protocol = testProtocol
		}
		s, err := UpgradeWebTransport(r, protocol)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		// the session is the request, held open until the client ends it
		for {
			str, err := s.AcceptStream(context.Background())
			if err != nil {
				return
			}
			b := make([]byte, len(testPing))
			if _, err = io.ReadFull(str, b); err == nil {
				_, _ = str.Write(b)
			}
			_ = str.Close()
		}
	})
}

func next(t *testing.T, seen <-chan string) string {
	t.Helper()
	select {
	case v := <-seen:
		return v
	case <-time.After(testDialTimeout):
		t.Fatal("no session request arrived")
		return ""
	}
}

func TestWebTransportSessions(t *testing.T) {
	seen := make(chan string, 4)
	e := startWTEndpoint(t, echoSessions(t, seen))
	if !e.h.WebTransportEnabled() {
		t.Fatal("WebTransport was enabled")
	}
	rsp, s, err := e.dial(t, testProtocol, "moqt-20")
	if err != nil {
		t.Fatal(err)
	}
	if got := next(t, seen); got != testListener {
		t.Errorf("the session names its listener, got %q", got)
	}
	if rsp.Header.Get(HeaderWTProtocol) != `"`+testProtocol+`"` ||
		s.SessionState().ApplicationProtocol != testProtocol {
		t.Errorf("the chosen protocol is answered as WT-Protocol: %q", rsp.Header.Get(HeaderWTProtocol))
	}
	// a session is no request body, so the read deadline on one never reaches it
	time.Sleep(pastDeadline)
	ctx, cancel := context.WithTimeout(context.Background(), testDialTimeout)
	defer cancel()
	str, err := s.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(str, testPing); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(str); err != nil || string(b) != testPing {
		t.Errorf("echo %q %v", b, err)
	}
	_ = s.CloseWithError(0, "")

	// a client offering nothing gets a session without WT-Protocol
	rsp, s, err = e.dial(t)
	if err != nil {
		t.Fatal(err)
	}
	next(t, seen)
	if _, ok := rsp.Header[HeaderWTProtocol]; ok || s.SessionState().ApplicationProtocol != "" {
		t.Error("no protocol was negotiated")
	}
	_ = s.CloseWithError(0, "")

	// plain requests are served on a connection that carries sessions
	tr := &webtransport.Transport{TLSClientConfig: e.tlsConfig()}
	qconn, err := quic.DialAddr(ctx, e.addr, e.tlsConfig(), &quic.Config{
		EnableDatagrams: true, EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := tr.NewClientConn(qconn)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+e.addr+"/", nil)
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "plain" {
		t.Errorf("plain request got %q", body)
	}
	_ = qconn.CloseWithError(0, "")

	// connections opened once WebTransport is off are served as plain HTTP/3, which announces no WebTransport
	e.h.SetWebTransport(false, "")
	if _, _, err = e.dial(t, testProtocol); err == nil {
		t.Error("a connection served without WebTransport refuses the session")
	}
}

func TestWebTransportShutdown(t *testing.T) {
	seen := make(chan string, 4)
	e := startWTEndpoint(t, echoSessions(t, seen))
	_, busy, err := e.dial(t, testProtocol)
	if err != nil {
		t.Fatal(err)
	}
	next(t, seen)
	ctx, cancel := context.WithTimeout(context.Background(), testDialTimeout)
	defer cancel()
	idle, err := quic.DialAddr(ctx, e.addr, e.tlsConfig(), &quic.Config{
		EnableDatagrams: true, EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := (&webtransport.Transport{TLSClientConfig: e.tlsConfig()}).NewClientConn(idle)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+e.addr+"/", nil)
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// the idle connection closes at once; the one carrying a session runs until the drain gives up
	drain, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- e.h.Shutdown(drain) }()
	select {
	case <-idle.Context().Done():
	case <-time.After(testDialTimeout):
		t.Fatal("an idle connection closes as the drain begins")
	}
	if err = <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a session outlasting the drain is cut when it ends: %v", err)
	}
	select {
	case <-busy.Context().Done():
	case <-time.After(testDialTimeout):
		t.Fatal("the drain closes what it waited for")
	}

	// a handler that never served through WebTransport drains at once
	if err = NewHandler(http.NotFoundHandler(), listener.ServerLimits{}).Shutdown(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestWebTransportDrainCompletes(t *testing.T) {
	seen := make(chan string, 4)
	e := startWTEndpoint(t, echoSessions(t, seen))
	_, s, err := e.dial(t, testProtocol)
	if err != nil {
		t.Fatal(err)
	}
	next(t, seen)
	done := make(chan error, 1)
	go func() { done <- e.h.Shutdown(context.Background()) }()
	_ = s.CloseWithError(0, "")
	select {
	case err = <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(testDialTimeout):
		t.Fatal("the drain finishes when the last session ends")
	}
	// a connection arriving after the drain is turned away
	if _, _, err = e.dial(t, testProtocol); err == nil {
		t.Error("a drained endpoint opens no session")
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	set int
}

func (d *deadlineRecorder) SetReadDeadline(time.Time) error {
	d.set++
	return nil
}

func TestRequestDeadlineSkipsConnect(t *testing.T) {
	h := requestDeadline(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), time.Second)
	post := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "http://"+appinfo.Domain+"/", strings.NewReader("body")))
	connect := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest(http.MethodConnect, "http://"+appinfo.Domain+"/moq", strings.NewReader("tunnel"))
	r.Proto = protocolWebTransport
	h.ServeHTTP(connect, r)
	if post.set != 1 || connect.set != 0 {
		t.Errorf("a body is bounded by the deadline but a tunnel is not: post %d, connect %d", post.set, connect.set)
	}
}

func TestQUICConfigCarriesWebTransport(t *testing.T) {
	if qc := QUICConfig(); !qc.EnableStreamResetPartialDelivery {
		t.Error("WebTransport needs stream resets with partial delivery")
	}
}
