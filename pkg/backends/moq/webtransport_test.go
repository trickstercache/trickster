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

package moq

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	pl "github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	listenerhttp3 "github.com/trickstercache/trickster/v2/pkg/proxy/listener/http3"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/quicmux"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	webListener = "web"
	alpn21      = "moqt-21"
	alpn20      = "moqt-20"
)

func startWTEndpoint(t *testing.T, h http.Handler) string {
	t.Helper()
	handler := listenerhttp3.NewHandler(h, pl.ServerLimits{})
	handler.SetWebTransport(true, webListener)
	m, err := quicmux.Join("127.0.0.1", 0, quicmux.MemberOptions{
		Name: webListener, ALPNs: []string{listenerhttp3.ALPN}, Handler: handler,
		Certs: sw.NewSwapper([]tls.Certificate{testCert(t)}), TLS: &tls.Config{MinVersion: tls.VersionTLS13},
		QUIC: listenerhttp3.QUICConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = m.Serve() }()
	t.Cleanup(func() { _ = m.Close() })
	return m.LocalAddr().String()
}

func dialWT(t *testing.T, addr string, header http.Header, protocols ...string) (*http.Response, *session.Session,
	error,
) {
	t.Helper()
	tr := &webtransport.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}, // #nosec G402 -- loopback test
			MinVersion: tls.VersionTLS13,
		},
		ApplicationProtocols: protocols,
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	rsp, ws, err := tr.Dial(ctx, "https://"+addr+"/live", header)
	if err != nil {
		return rsp, nil, err
	}
	// a session that negotiated nothing speaks the draft the server assumed
	c, ok := codec.ForALPN(ws.SessionState().ApplicationProtocol)
	if !ok {
		c, _ = codec.ForDraft(21)
	}
	s, _ := session.New(session.FromWebTransport(ws), session.Options{
		Role: session.RoleClient, Codec: c,
		WebTransport: true,
	})
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	return rsp, s, s.Start(ctx)
}

func startWTOrigin(t *testing.T, negotiate bool) *origin {
	t.Helper()
	o := &origin{seen: make(chan *message.Setup, 8)}
	o.addr = startWTEndpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol := ""
		offered := offeredProtocols(r.Header[http.CanonicalHeaderKey(listenerhttp3.HeaderWTAvailableProtocols)])
		if negotiate && len(offered) > 0 {
			protocol = offered[0]
		}
		c, ok := codec.ForALPN(protocol)
		if !ok {
			c, _ = codec.ForDraft(21)
		}
		ws, err := listenerhttp3.UpgradeWebTransport(r, protocol)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s, _ := session.New(session.FromWebTransport(ws), session.Options{
			Role: session.RoleServer, Codec: c, WebTransport: true, Handler: o,
			OnSetup: func(_ *session.Session, m *message.Setup) error { o.seen <- m; return nil },
		})
		if s.Start(context.Background()) == nil {
			<-s.Done()
		}
	}))
	return o
}

func TestWebTransportDownstream(t *testing.T) {
	org := startOrigin(t)
	client := newBackend(t, "wt-down", "moqt://"+org.addr+"/live")
	addr := startWTEndpoint(t, http.HandlerFunc(client.serveWebTransport))
	ok := metrics.MoQSessions.WithLabelValues(webListener, "wt-down", roleDownstream, transportWebTransport, "21",
		resultOK)
	before := testutil.ToFloat64(ok)
	rsp, s, err := dialWT(t, addr, nil, alpn20, alpn21)
	if err != nil {
		t.Fatal(err)
	}
	if rsp.Header.Get(listenerhttp3.HeaderWTProtocol) != `"`+alpn21+`"` {
		t.Errorf("the backend's preference picks the draft: %q", rsp.Header.Get(listenerhttp3.HeaderWTProtocol))
	}
	got := subscribe(t, s)
	if setup := recv(t, org.seen, "the upstream SETUP"); setup.Path != "/live" {
		t.Errorf("upstream SETUP %+v", setup)
	}
	org.publish(t, 1, "key", "delta")
	if a, b := recv(t, got.got, "object"), recv(t, got.got, "object"); a != "key" || b != "delta" {
		t.Errorf("objects %q %q", a, b)
	}
	if testutil.ToFloat64(ok) != before+1 {
		t.Error("the session is counted as a WebTransport session")
	}
	if client.sh.wtActive.Load() != 1 {
		t.Error("the open session counts against the backend's limit")
	}
}

func TestWebTransportUpstream(t *testing.T) {
	org := startWTOrigin(t, true)
	client := newBackend(t, "wt-up", "https://"+org.addr+"/live")
	st := startListener(t, mo.NewListener(), client)
	s, err := dial(t, st.addr, "relay.example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := subscribe(t, s)
	if setup := recv(t, org.seen, "the upstream SETUP"); setup.HasPath || setup.HasAuthority {
		t.Errorf("a SETUP over WebTransport names no path or authority: %+v", setup)
	}
	org.publish(t, 4, "over", "webtransport")
	if a, b := recv(t, got.got, "object"), recv(t, got.got, "object"); a != "over" || b != "webtransport" {
		t.Errorf("objects %q %q", a, b)
	}
	if err = client.HealthCheckProbe()(context.Background()); err != nil {
		t.Errorf("a WebTransport origin passes its probe: %v", err)
	}
}

func TestWebTransportOriginWithoutProtocol(t *testing.T) {
	org := startWTOrigin(t, false)
	client := newBackend(t, "wt-legacy", "https://"+org.addr+"/live")
	if err := client.HealthCheckProbe()(context.Background()); !errors.Is(err, errNoDraft) {
		t.Errorf("an origin that negotiates no draft is refused: %v", err)
	}
	assumed := newBackendWith(t, "wt-assumed", "https://"+org.addr+"/live", func(o *bo.Options) {
		o.MoQ.WebTransport.DefaultDraft = 21
	})
	if err := assumed.HealthCheckProbe()(context.Background()); err != nil {
		t.Errorf("a default draft speaks to an origin that negotiates none: %v", err)
	}
}

func TestWebTransportRefusals(t *testing.T) {
	org := startOrigin(t)
	// each configuration is its own backend, as a reload makes it, so no request sees options change under it
	serve := func(name string, mutate func(*mo.WebTransportOptions)) (*Client, string) {
		client := newBackendWith(t, name, "moqt://"+org.addr+"/live", func(o *bo.Options) {
			if mutate != nil {
				mutate(o.MoQ.WebTransport)
			}
		})
		return client, startWTEndpoint(t, http.HandlerFunc(client.serveWebTransport))
	}
	status := func(addr string, header http.Header, protocols ...string) int {
		t.Helper()
		rsp, _, err := dialWT(t, addr, header, protocols...)
		if rsp == nil {
			t.Fatalf("no response: %v", err)
		}
		return rsp.StatusCode
	}

	client, addr := serve("wt-refuse", nil)
	rec := httptest.NewRecorder()
	client.serveWebTransport(rec, httptest.NewRequest(http.MethodGet, "/live", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get(headers.NameAllow) != http.MethodConnect {
		t.Errorf("a GET is not allowed: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	client.serveWebTransport(rec, httptest.NewRequest(http.MethodConnect, "/live", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a CONNECT that is no WebTransport request is refused: %d", rec.Code)
	}
	foreign := http.Header{headers.NameOrigin: {"https://player.example"}}
	if got := status(addr, foreign, alpn21); got != http.StatusForbidden {
		t.Errorf("a foreign origin is refused by default: %d", got)
	}
	if got := status(addr, http.Header{headers.NameOrigin: {"https://" + addr}}, alpn21); got != http.StatusOK {
		t.Errorf("the same origin is allowed: %d", got)
	}
	if got := status(addr, nil, "moqt-99"); got != http.StatusBadRequest {
		t.Errorf("a client offering no supported draft is refused: %d", got)
	}
	if got := status(addr, nil); got != http.StatusBadRequest {
		t.Errorf("a client offering no protocol is refused unless a default draft is set: %d", got)
	}

	_, addr = serve("wt-origins", func(w *mo.WebTransportOptions) { w.AllowedOrigins = []string{"https://player.example"} })
	if got := status(addr, foreign, alpn21); got != http.StatusOK {
		t.Errorf("an allowed origin opens a session: %d", got)
	}
	_, addr = serve("wt-default", func(w *mo.WebTransportOptions) { w.DefaultDraft = 21 })
	if got := status(addr, nil); got != http.StatusOK {
		t.Errorf("a default draft admits a client offering no protocol: %d", got)
	}
	_, addr = serve("wt-busy", func(w *mo.WebTransportOptions) { w.MaxSessions = 1 })
	if got := status(addr, nil, alpn21); got != http.StatusOK {
		t.Fatalf("the first session opens: %d", got)
	}
	if got := status(addr, nil, alpn21); got != http.StatusServiceUnavailable {
		t.Errorf("a session past max_sessions is refused: %d", got)
	}
}

func TestOriginAllowed(t *testing.T) {
	r := httptest.NewRequest(http.MethodConnect, "https://live.example.com/live", nil)
	r.Host = "live.example.com"
	tests := []struct {
		origin  string
		allowed []string
		want    bool
	}{
		{"", nil, true},
		{"https://live.example.com", nil, true},
		{"https://LIVE.example.com", nil, true},
		{"https://other.example.com", nil, false},
		{"null", nil, false},
		{"https://player.example", []string{"https://player.example"}, true},
		{"HTTPS://Player.Example/", []string{"https://player.example"}, true},
		{"https://player.example:8443", []string{"https://player.example"}, false},
		{"https://anything.example", []string{mo.AnyOrigin}, true},
		{"null", []string{"https://player.example"}, false},
	}
	for _, tc := range tests {
		r.Header.Set(headers.NameOrigin, tc.origin)
		if got := originAllowed(r, tc.allowed); got != tc.want {
			t.Errorf("origin %q with %v: got %t", tc.origin, tc.allowed, got)
		}
	}
}

func TestSelectDraft(t *testing.T) {
	o := mo.New().WebTransport
	h := http.Header{}
	h.Set(listenerhttp3.HeaderWTAvailableProtocols, `"moqt-20", "moqt-21"`)
	if c, p, ok := selectDraft(h, o); !ok || c.Draft() != 21 || p != alpn21 {
		t.Errorf("the backend's preference wins: %v %q", c, p)
	}
	o.Drafts = []int{20}
	if c, _, ok := selectDraft(h, o); !ok || c.Draft() != 20 {
		t.Error("only configured drafts are chosen")
	}
	h.Set(listenerhttp3.HeaderWTAvailableProtocols, `moqt-21`)
	if _, _, ok := selectDraft(h, o); ok {
		t.Error("a protocol list that does not parse offers nothing usable")
	}
	h.Set(listenerhttp3.HeaderWTAvailableProtocols, `"moqt-21", ?1, "moqt-20"`)
	if c, _, ok := selectDraft(h, o); !ok || c.Draft() != 20 {
		t.Error("members that are not strings are skipped")
	}
	h.Del(listenerhttp3.HeaderWTAvailableProtocols)
	if _, _, ok := selectDraft(h, o); ok {
		t.Error("no offer and no default draft")
	}
	o.DefaultDraft = 20
	if c, p, ok := selectDraft(h, o); !ok || c.Draft() != 20 || p != "" {
		t.Error("the default draft, answered with no WT-Protocol")
	}
	if c, ok := negotiated("", 0); ok || c != nil {
		t.Error("no protocol and no default")
	}
	if c, ok := negotiated(alpn20, 0); !ok || c.Draft() != 20 {
		t.Error("a negotiated protocol")
	}
	if c, ok := negotiated("", 21); !ok || c.Draft() != 21 {
		t.Error("an assumed default")
	}
}

func TestHTTPRoutes(t *testing.T) {
	org := startOrigin(t)
	client := newBackend(t, "wt-routes", "moqt://"+org.addr+"/live")
	if client.Handlers()["moq"] == nil {
		t.Error("the WebTransport handler is registered")
	}
	paths := client.DefaultPathConfigs(client.Configuration())
	if len(paths) != 1 || paths[0].Path != rootPath || paths[0].HandlerName != "moq" ||
		paths[0].Methods[0] != http.MethodConnect || paths[0].MatchType != matching.PathMatchTypeSegment {
		t.Errorf("a backend with no paths accepts sessions everywhere: %+v", paths)
	}
	o := bo.New()
	o.Paths = po.List{po.New(), &po.Options{Path: "/hello", HandlerName: "localresponse"}, nil}
	if client.DefaultPathConfigs(o) != nil {
		t.Error("configured paths alone decide which sessions the backend serves")
	}
	webTransportPaths(o)
	if o.Paths[0].HandlerName != "moq" || o.Paths[0].Methods[0] != http.MethodConnect ||
		o.Paths[1].HandlerName != "localresponse" {
		t.Errorf("proxy paths become session paths, local ones stay: %+v %+v", o.Paths[0], o.Paths[1])
	}
	if client.DefaultHealthCheckConfig() == nil {
		t.Error("health check defaults")
	}

	c := config.NewConfig()
	c.Backends = bo.Lookup{"wt-routes": client.Configuration()}
	if !ServesWebTransport(c, "moq1") || ServesWebTransport(c, "default") || ServesWebTransport(nil, "moq1") {
		t.Error("a listener carries WebTransport when a moq backend uses it")
	}

	bare := bo.New()
	bare.Name, bare.Provider = "wt-bare", "moq"
	b, err := NewClient("wt-bare", bare, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.(*Client).Close)
	if w := b.(*Client).webTransportOptions(); w == nil || w.MaxSessions != mo.DefaultMaxSessions {
		t.Error("a backend without a moq block takes the WebTransport defaults")
	}
}

func TestImplementation(t *testing.T) {
	if implementation("relay/1") != "relay/1" || implementation("") != Implementation {
		t.Error("a configured implementation wins, else Trickster's")
	}
	saved := appinfo.Version
	t.Cleanup(func() { appinfo.Version = saved })
	appinfo.Version = "2.2.0"
	if implementation("") != Implementation+"/2.2.0" {
		t.Errorf("the version is appended: %q", implementation(""))
	}
}
