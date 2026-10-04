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
	"io"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	pl "github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
)

const wait = 5 * time.Second

func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, cert, err := testtls.GetTestKeyAndCertWithNames("localhost")
	if err != nil {
		t.Fatal(err)
	}
	kp, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

type origin struct {
	addr string
	mu   sync.Mutex
	subs []*session.Request
	seen chan *message.Setup
}

func (o *origin) HandleRequest(r *session.Request) {
	r.SetHandlers(nopHandler{}, nil)
	switch r.Message().(type) {
	case *message.Subscribe:
		o.mu.Lock()
		o.subs = append(o.subs, r)
		o.mu.Unlock()
		_ = r.Send(&message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias()})
	default:
		_ = r.Reject(message.RequestNotSupported, "", 0)
	}
}

func (o *origin) HandleGoAway(*session.Session, *message.GoAway) {}

func (o *origin) publish(t *testing.T, group uint64, payloads ...string) {
	t.Helper()
	o.mu.Lock()
	subs := o.subs
	o.mu.Unlock()
	for _, r := range subs {
		out, err := r.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: group, Priority: 1, EndOfGroup: true},
			3, false)
		if err != nil {
			continue
		}
		for i, p := range payloads {
			if err = out.WriteObject(&message.SubgroupObject{ID: uint64(i), PayloadLen: uint64(len(p))}); err != nil {
				t.Fatal(err)
			}
			if _, err = out.Write([]byte(p)); err != nil {
				t.Fatal(err)
			}
		}
		_ = out.Close()
	}
}

type nopHandler struct{}

func (nopHandler) HandleMessage(*session.Request, message.Message) {}
func (nopHandler) HandleClose(*session.Request, error)             {}

func startOrigin(t *testing.T) *origin {
	t.Helper()
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{testCert(t)}, NextProtos: codec.ALPNs(codec.ServerPreference),
		MinVersion: tls.VersionTLS13,
	}, &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	o := &origin{addr: ln.Addr().String(), seen: make(chan *message.Setup, 8)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
			s, _ := session.New(session.FromQUIC(conn), session.Options{
				Role: session.RoleServer, Codec: c, Handler: o,
				OnSetup: func(_ *session.Session, m *message.Setup) error { o.seen <- m; return nil },
			})
			go func() { _ = s.Start(context.Background()) }()
		}
	}()
	return o
}

type stack struct {
	client *Client
	member pl.PacketMember
	addr   string
	srv    *server
}

func newBackend(t *testing.T, name, origin string) *Client {
	t.Helper()
	return newBackendWith(t, name, origin, nil)
}

func newBackendWith(t *testing.T, name, origin string, mutate func(*bo.Options)) *Client {
	t.Helper()
	o := bo.New()
	o.Name, o.Provider, o.OriginURL, o.ListenerNames = name, "moq", origin, []string{"moq1"}
	o.TLS = &to.Options{InsecureSkipVerify: true}
	o.MoQ = mo.New()
	if mutate != nil {
		mutate(o)
	}
	if err := NativeListenerAdapter().ValidateBackend(o); err != nil {
		t.Fatal(err)
	}
	b, err := NewClient(name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Cleanup(c.Close)
	return c
}

func startListener(t *testing.T, lo *mo.ListenerOptions, clients ...*Client) *stack {
	t.Helper()
	c := config.NewConfig()
	opts := &listener.Options{Protocol: listener.ProtocolMoQ, ListenAddress: "127.0.0.1", MoQ: lo}
	c.Listeners["moq1"] = opts
	all := backends.Backends{}
	c.Backends = bo.Lookup{}
	for _, cl := range clients {
		all[cl.Name()] = cl
		c.Backends[cl.Name()] = cl.Configuration()
	}
	a := NativeListenerAdapter()
	if err := a.ValidateListener(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Describe(c, "moq1"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Build(native.BuildRequest{}); err == nil {
		t.Error("a moq listener is never built as a TCP server")
	}
	kp := testCert(t)
	m, err := a.(native.PacketAdapter).Join(native.BuildRequest{
		Config: c, ListenerName: "moq1", Listener: opts,
		BackendClients: all,
	}, "listener.moq1.moq", sw.NewSwapper([]tls.Certificate{kp}),
		&tls.Config{MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = m.Serve() }()
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	srv := m.(interface{ ProtocolHandler() any }).ProtocolHandler().(*server)
	return &stack{member: m, addr: m.(interface{ LocalAddr() net.Addr }).LocalAddr().String(), srv: srv}
}

func dial(t *testing.T, addr, authority string) (*session.Session, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"moqt-21"}, // #nosec G402
		MinVersion: tls.VersionTLS13}, &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		return nil, err
	}
	c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	s, _ := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c,
		Setup: message.Setup{Path: "/", HasPath: true, Authority: authority, HasAuthority: true},
	})
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	return s, s.Start(ctx)
}

type objects struct {
	got chan string
}

func (o *objects) HandleMessage(*session.Request, message.Message)      {}
func (o *objects) HandleClose(*session.Request, error)                  {}
func (o *objects) HandleDatagram(*session.Request, *message.Datagram)   {}
func (o *objects) HandleFetch(*session.Request, *session.IncomingFetch) {}

func (o *objects) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	for {
		if _, err := in.Next(); err != nil {
			return
		}
		b, _ := io.ReadAll(in)
		o.got <- string(b)
	}
}

func subscribe(t *testing.T, s *session.Session) *objects {
	t.Helper()
	o := &objects{got: make(chan string, 16)}
	track := message.FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte("video")}
	req, err := s.Open(context.Background(), &message.Subscribe{Track: track}, o, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	m, err := req.Response(ctx)
	if err != nil || m.Kind() != message.KindSubscribeOK {
		t.Fatalf("subscription refused: %v %v", m, err)
	}
	return o
}

func TestRelayThroughListener(t *testing.T) {
	org := startOrigin(t)
	client := newBackend(t, "live", "moqt://"+org.addr+"/live")
	st := startListener(t, mo.NewListener(), client)
	s, err := dial(t, st.addr, "relay.example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := subscribe(t, s)
	setup := recv(t, org.seen, "the upstream SETUP")
	if setup.Path != "/live" || setup.Implementation != Implementation {
		t.Errorf("upstream SETUP %+v", setup)
	}
	org.publish(t, 1, "key", "delta")
	if a, b := recv(t, got.got, "object"), recv(t, got.got, "object"); a != "key" || b != "delta" {
		t.Errorf("objects %q %q", a, b)
	}
	if err = client.HealthCheckProbe()(context.Background()); err != nil {
		t.Errorf("a reachable origin passes its probe: %v", err)
	}
	if client.HealthCheckUnsupported() != "" || client.Relay() == nil || client.Scope().Path != "/live" {
		t.Error("client accessors")
	}
	if err = st.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Error("stopping the listener ends its sessions")
	}
}

func TestListenerRefusals(t *testing.T) {
	org := startOrigin(t)
	a := newBackend(t, "a", "moqt://"+org.addr+"/a")
	a.Configuration().Hosts = []string{"a.example.com"}
	b := newBackend(t, "b", "moqt://"+org.addr+"/b")
	b.Configuration().Hosts = []string{"b.example.com"}
	lo := mo.NewListener()
	lo.MaxSessions = 1
	st := startListener(t, lo, a, b)

	s, err := dial(t, st.addr, "c.example.com")
	if err == nil {
		<-s.Done()
		err = s.Err()
	}
	if ce, ok := errors.AsType[*session.CloseError](err); !ok || ce.Code != uint64(message.CodeInvalidPath) {
		t.Errorf("a host no backend serves is refused: %v", err)
	}

	if _, err = dial(t, st.addr, "a.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err = dial(t, st.addr, "b.example.com"); err == nil {
		t.Error("a session past max_sessions is refused")
	}
}

type denyAll struct{}

func (denyAll) Admit(netip.Addr) *backends.Denial {
	return &backends.Denial{Reason: backends.DenialLocation, Message: "not here"}
}

func TestListenerUpdates(t *testing.T) {
	org := startOrigin(t)
	client := newBackend(t, "live", "moqt://"+org.addr+"/live")
	st := startListener(t, mo.NewListener(), client)
	if st.srv.ProtocolRestartKey() == "" {
		t.Error("a restart key")
	}
	st.srv.UpdateSessionGate(denyAll{})
	if _, err := dial(t, st.addr, "x"); err == nil {
		t.Error("a gated listener refuses the session")
	}
	st.srv.UpdateSessionGate(nil)
	// a list with no entries takes its default verdict, deny, for every address
	list, _, err := ipacl.Compile(ipacl.Options{Name: "deny-all"})
	if err != nil {
		t.Fatal(err)
	}
	st.srv.UpdateIPACL(list)
	if _, err = dial(t, st.addr, "x"); err == nil {
		t.Error("a denied address is refused")
	}
	st.srv.UpdateIPACL(nil)
	st.srv.UpdateRouteResolver(&routeTable{})
	s, err := dial(t, st.addr, "x")
	if err == nil {
		<-s.Done()
	}
	st.srv.UpdateRouteResolver(routesFor(native.BuildRequest{}))
}

func TestOriginlessAndDialErrors(t *testing.T) {
	o := bo.New()
	o.Name, o.Provider, o.ListenerNames = "studio", "moq", []string{"moq1"}
	b, err := NewClient("studio", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	defer c.Close()
	if c.HealthCheckUnsupported() == "" || c.HealthCheckProbe()(context.Background()) == nil {
		t.Error("an origin-less backend has nothing to probe")
	}
	for _, raw := range []string{"http://x", "::bad"} {
		if _, err = parseOrigin(raw); err == nil {
			t.Errorf("%q is not a moq origin", raw)
		}
	}
	u, _ := parseOrigin("https://relay.example/moq")
	if wd, err := newDialer(u, nil, codec.ClientOffer, 0, ""); err != nil || !wd.webTransport || wd.setup.HasPath {
		t.Errorf("a WebTransport origin is dialed by WebTransport, its SETUP naming no path: %v", err)
	}
	if _, err = newDialer(&url.URL{Scheme: "ftp", Host: "relay.example"}, nil, codec.ClientOffer, 0, ""); err == nil {
		t.Error("only moqt and https origins are dialed")
	}
	u, _ = parseOrigin("moqt:///nohost")
	if _, err = newDialer(u, nil, codec.ClientOffer, 0, ""); err == nil {
		t.Error("an origin needs a host")
	}
	u, _ = parseOrigin("moqt://127.0.0.1:1/")
	d, err := newDialer(u, nil, codec.ClientOffer, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err = d.session(ctx); err == nil {
		t.Error("an unreachable origin fails to dial")
	}
	d.close()
	if _, _, err = d.Session(context.Background(), c.Scope(), message.FullTrackName{}); !errors.Is(err, errDialerClosed) {
		t.Errorf("a closed dialer dials nothing: %v", err)
	}
}

func TestRelaysSurviveReloads(t *testing.T) {
	org := startOrigin(t)
	origin := "moqt://" + org.addr + "/live"
	first := newBackend(t, "reloaded", origin)
	second := newBackend(t, "reloaded", origin)
	if first.Relay() != second.Relay() {
		t.Fatal("a reload with the same settings keeps the backend's relay")
	}
	changed := bo.New()
	changed.Name, changed.Provider, changed.OriginURL, changed.ListenerNames = "reloaded", "moq", origin, []string{"moq1"}
	changed.TLS = &to.Options{InsecureSkipVerify: true}
	changed.MoQ = mo.New()
	changed.MoQ.LiveEdge.Groups = 9
	b, err := NewClient("reloaded", changed, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	third := b.(*Client)
	t.Cleanup(third.Close)
	if third.Relay() == second.Relay() {
		t.Fatal("changed settings get a new relay")
	}
	// a session still on the old relay keeps it open through the reload's retirement
	second.sh.hold()
	RetireClients(backends.Backends{"reloaded": third})
	if second.sh.isClosed() {
		t.Fatal("a retired relay serves its sessions to their end")
	}
	second.sh.drop()
	if !second.sh.isClosed() {
		t.Error("a retired relay closes when its last session ends")
	}
	if third.sh.isClosed() {
		t.Error("the committed client's relay stays")
	}
	StopClients()
	if !third.sh.isClosed() {
		t.Error("stopping closes every relay")
	}
	if third.sh.Cache() != nil {
		t.Error("a client built with no cache spills nowhere")
	}
}

func TestDialerSharesOneSession(t *testing.T) {
	org := startOrigin(t)
	client := newBackend(t, "shared-dial", "moqt://"+org.addr+"/live")
	d := client.sh.dialer
	const callers = 8
	got := make(chan *session.Session, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			s, err := d.session(context.Background())
			if err != nil {
				t.Error(err)
			}
			got <- s
		})
	}
	wg.Wait()
	close(got)
	first := <-got
	for s := range got {
		if s != first {
			t.Fatal("concurrent callers share the one upstream session")
		}
	}
	// a client's GOAWAY reaches the backend's relay through the session's route and leaves its subscriptions running
	st := startListener(t, mo.NewListener(), client)
	s, err := dial(t, st.addr, "relay.example.com")
	if err != nil {
		t.Fatal(err)
	}
	objs := subscribe(t, s)
	if err = s.GoAway("", 0); err != nil {
		t.Fatal(err)
	}
	org.publish(t, 7, "after-goaway")
	if got := recv(t, objs.got, "object"); got != "after-goaway" {
		t.Errorf("object %q", got)
	}
}

func TestAccessCommittedOnReload(t *testing.T) {
	open := newBackendWith(t, "ingest", "", func(o *bo.Options) { o.MoQ.Publish.Enabled = true })
	st := startListener(t, mo.NewListener(), open)
	s, err := dial(t, st.addr, "x")
	if err != nil {
		t.Fatal(err)
	}
	publish := func(name string) message.Message {
		t.Helper()
		req, err := s.Open(context.Background(), &message.Publish{
			Track:      message.FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte(name)},
			TrackAlias: s.NewTrackAlias(),
		}, nopHandler{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		m, err := req.Response(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if _, ok := publish("cam1").(*message.RequestOK); !ok {
		t.Fatal("a backend that accepts publishers takes a PUBLISH")
	}
	closed := newBackendWith(t, "ingest", "", func(o *bo.Options) { o.MoQ.Publish.Enabled = false })
	if closed.Relay() != open.Relay() {
		t.Fatal("a reload that changes only the access policy keeps the relay")
	}
	if _, ok := publish("cam2").(*message.RequestOK); !ok {
		t.Error("the policy changes only when the reload commits")
	}
	RetireClients(backends.Backends{"ingest": closed})
	if re, ok := publish("cam3").(*message.RequestError); !ok || re.Code != message.RequestNotSupported {
		t.Errorf("the committed policy refuses publishers: %+v", re)
	}

	o := bo.New()
	o.Name, o.Provider, o.ListenerNames = "badtokens", "moq", []string{"moq1"}
	o.MoQ = mo.New()
	o.MoQ.Authorization.Mode, o.MoQ.Authorization.TokenFile = mo.AuthorizationToken, "/nonexistent/tokens"
	if _, err = NewClient("badtokens", o, nil, nil, nil, nil); err == nil {
		t.Error("an unreadable token file fails the backend")
	}
}
