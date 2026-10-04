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

package integration

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	moqListener = "moq1"
	moqWait     = 10 * time.Second
	moqTrack    = "video"
)

type moqObject struct {
	id      uint64
	payload []byte
}

// moqOrigin publishes tracks over native QUIC, answering SUBSCRIBE and FETCH and counting both
type moqOrigin struct {
	addr       string
	mu         sync.Mutex
	subs       map[string][]*session.Request
	groups     map[uint64][]moqObject
	subscribes atomic.Int32
	fetches    atomic.Int32
}

func startMoQOrigin(t *testing.T) *moqOrigin {
	t.Helper()
	keyPEM, certPEM, err := testtls.GetTestKeyAndCertWithNames("localhost")
	require.NoError(t, err)
	kp, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{kp},
		NextProtos:   codec.ALPNs(codec.ServerPreference), MinVersion: tls.VersionTLS13,
	},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	o := &moqOrigin{
		addr: ln.Addr().String(), subs: make(map[string][]*session.Request),
		groups: make(map[uint64][]moqObject),
	}
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
			var path string
			s, _ := session.New(session.FromQUIC(conn), session.Options{
				Role: session.RoleServer, Codec: c,
				Handler: &moqOriginSession{o: o, path: &path},
				OnSetup: func(_ *session.Session, m *message.Setup) error { path = m.Path; return nil },
			})
			go func() { _ = s.Start(context.Background()) }()
		}
	}()
	return o
}

type moqOriginSession struct {
	o    *moqOrigin
	path *string
}

func (h *moqOriginSession) HandleGoAway(*session.Session, *message.GoAway) {}

func (h *moqOriginSession) HandleRequest(r *session.Request) {
	r.SetHandlers(moqNop{}, nil)
	o := h.o
	switch m := r.Message().(type) {
	case *message.Subscribe:
		o.subscribes.Add(1)
		o.mu.Lock()
		o.subs[*h.path] = append(o.subs[*h.path], r)
		o.mu.Unlock()
		_ = r.Send(&message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias()})
	case *message.Fetch:
		o.fetches.Add(1)
		o.fetch(r, m)
	default:
		_ = r.Reject(message.RequestNotSupported, "", 0)
	}
}

func (o *moqOrigin) fetch(r *session.Request, m *message.Fetch) {
	o.mu.Lock()
	var largest wire.Location
	for g, objs := range o.groups {
		if l := (wire.Location{Group: g, Object: objs[len(objs)-1].id}); largest.Less(l) {
			largest = l
		}
	}
	start, end, _, _ := m.Params.LocationFilter.Bounds(&largest, true)
	type entry struct {
		g  uint64
		ob moqObject
	}
	var entries []entry
	for g := start.Group; g <= end.Group && g <= largest.Group; g++ {
		for _, ob := range o.groups[g] {
			if l := (wire.Location{Group: g, Object: ob.id}); !l.Less(start) && !end.Less(l) {
				entries = append(entries, entry{g, ob})
			}
		}
	}
	o.mu.Unlock()
	if r.Send(&message.FetchOK{EndLocation: end}) != nil {
		return
	}
	out, err := r.OpenFetch(context.Background(), r.RequestID(), message.GroupOrderAscending, 3)
	if err != nil {
		return
	}
	for _, e := range entries {
		fo := message.FetchObject{Group: e.g, Object: e.ob.id, Priority: 1, PayloadLen: uint64(len(e.ob.payload))}
		if out.WriteObject(&fo) != nil {
			return
		}
		if _, err = out.Write(e.ob.payload); err != nil {
			return
		}
	}
	_ = out.Close()
	_ = r.CloseSend()
}

// publish stores a group and sends it to every subscription made through path
func (o *moqOrigin) publish(t *testing.T, path string, group uint64, payloads ...string) {
	t.Helper()
	objs := make([]moqObject, len(payloads))
	for i, p := range payloads {
		objs[i] = moqObject{id: uint64(i), payload: []byte(p)}
	}
	o.mu.Lock()
	o.groups[group] = objs
	subs := slices.Clone(o.subs[path])
	o.mu.Unlock()
	for _, r := range subs {
		out, err := r.OpenSubgroup(context.Background(),
			message.SubgroupHeader{Group: group, Priority: 1, EndOfGroup: true}, 3, false)
		if err != nil {
			continue
		}
		for _, ob := range objs {
			require.NoError(t, out.WriteObject(&message.SubgroupObject{ID: ob.id, PayloadLen: uint64(len(ob.payload))}))
			_, err = out.Write(ob.payload)
			require.NoError(t, err)
		}
		_ = out.Close()
	}
}

type moqNop struct{}

func (moqNop) HandleMessage(*session.Request, message.Message) {}
func (moqNop) HandleClose(*session.Request, error)             {}

type moqReceived struct {
	objects chan string
	fetched chan string
	done    chan struct{}
}

func (r *moqReceived) HandleMessage(*session.Request, message.Message)    {}
func (r *moqReceived) HandleClose(*session.Request, error)                {}
func (r *moqReceived) HandleDatagram(*session.Request, *message.Datagram) {}

func (r *moqReceived) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	for {
		if _, err := in.Next(); err != nil {
			return
		}
		b, _ := io.ReadAll(in)
		r.objects <- string(b)
	}
}

func (r *moqReceived) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	defer close(r.done)
	for {
		if _, err := in.Next(); err != nil {
			return
		}
		b, _ := io.ReadAll(in)
		r.fetched <- string(b)
	}
}

func newMoQReceived() *moqReceived {
	return &moqReceived{objects: make(chan string, 64), fetched: make(chan string, 64), done: make(chan struct{})}
}

type moqBackend struct {
	name, path, host string
}

// moqHarness starts Trickster with a moq listener whose backends relay paths of origin
func moqHarness(t *testing.T, origin *moqOrigin, groups int, backends ...moqBackend) (tricksterHarness, string) {
	t.Helper()
	keyPEM, certPEM, err := testtls.GetTestKeyAndCertWithNames("localhost")
	require.NoError(t, err)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	udp, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "127.0.0.1", ListenPort: udp[0],
		}
		for _, mb := range backends {
			b := bo.New()
			b.Name, b.Provider, b.CacheName = mb.name, "moq", "default"
			b.OriginURL = fmt.Sprintf("moqt://%s%s", origin.addr, mb.path)
			b.ListenerNames = []string{moqListener}
			if mb.host != "" {
				b.Hosts = []string{mb.host}
			}
			b.TLS = &to.Options{
				ServeTLS: true, FullChainCertPath: certPath, PrivateKeyPath: keyPath,
				InsecureSkipVerify: true,
			}
			b.MoQ = mo.New()
			b.MoQ.LiveEdge.Groups = groups
			c.Backends[mb.name] = b
		}
	})
	releaseUDP()
	h.start(t)
	return h, fmt.Sprintf("127.0.0.1:%d", udp[0])
}

func dialMoQ(t *testing.T, addr, authority string) (*session.Session, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, // #nosec G402 -- test certificate
		NextProtos: codec.ALPNs(codec.ClientOffer), MinVersion: tls.VersionTLS13},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		return nil, err
	}
	c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	s, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c,
		Setup: message.Setup{Path: "/", HasPath: true, Authority: authority, HasAuthority: true},
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	if err = s.Start(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func moqTrackName() message.FullTrackName {
	return message.FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte(moqTrack)}
}

func moqSubscribe(t *testing.T, s *session.Session) *moqReceived {
	t.Helper()
	r := newMoQReceived()
	req, err := s.Open(context.Background(), &message.Subscribe{Track: moqTrackName()}, r, r)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	m, err := req.Response(ctx)
	require.NoError(t, err)
	require.Equal(t, message.KindSubscribeOK, m.Kind(), "subscription refused: %+v", m)
	return r
}

func moqNext(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(moqWait):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

func TestMoQRelay(t *testing.T) {
	origin := startMoQOrigin(t)
	h, addr := moqHarness(t, origin, 1, moqBackend{name: "moqlive", path: "/live"})

	first, err := dialMoQ(t, addr, "localhost")
	require.NoError(t, err)
	second, err := dialMoQ(t, addr, "localhost")
	require.NoError(t, err)
	a, b := moqSubscribe(t, first), moqSubscribe(t, second)
	require.Equal(t, int32(1), origin.subscribes.Load(), "two subscribers share one upstream subscription")

	for g := range uint64(3) {
		origin.publish(t, "/live", g, fmt.Sprintf("g%d-key", g), fmt.Sprintf("g%d-delta", g))
		for _, r := range []*moqReceived{a, b} {
			require.Equal(t, fmt.Sprintf("g%d-key", g), moqNext(t, r.objects, "a key object"))
			require.Equal(t, fmt.Sprintf("g%d-delta", g), moqNext(t, r.objects, "a delta object"))
		}
	}
	upstream, found := metricValue(t, h.MetricsAddr, "trickster_moq_active_subscriptions",
		`backend_name="moqlive",direction="upstream"`)
	require.True(t, found, "the upstream subscription gauge is exported")
	require.Equal(t, 1.0, upstream)

	// group 0 has left the one-group live edge for the cache, so a FETCH needs nothing upstream
	require.Eventually(t, func() bool {
		f := newMoQReceived()
		var p message.Params
		p.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStartGroupEnd})
		req, err := first.Open(context.Background(), &message.Fetch{Track: moqTrackName(), Params: p}, f, f)
		if err != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), moqWait)
		defer cancel()
		if m, err := req.Response(ctx); err != nil || m.Kind() != message.KindFetchOK {
			return false
		}
		<-f.done
		return len(f.fetched) == 2 && <-f.fetched == "g0-key" && <-f.fetched == "g0-delta"
	}, moqWait, 50*time.Millisecond)
	require.Equal(t, int32(0), origin.fetches.Load(), "the evicted group is served from the cache")
}

func TestMoQReloadKeepsSessions(t *testing.T) {
	guardSIGHUP(t)
	origin := startMoQOrigin(t)
	h, addr := moqHarness(t, origin, 3,
		moqBackend{name: "moqa", path: "/a", host: "a.example.com"},
		moqBackend{name: "moqb", path: "/b", host: "b.example.com"},
		moqBackend{name: "moqc", path: "/c", host: "c.example.com"})

	s, err := dialMoQ(t, addr, "b.example.com")
	require.NoError(t, err)
	r := moqSubscribe(t, s)
	origin.publish(t, "/b", 0, "before")
	require.Equal(t, "before", moqNext(t, r.objects, "an object before the reload"))

	data, err := os.ReadFile(h.ConfigPath)
	require.NoError(t, err)
	var c tkconfig.Config
	require.NoError(t, yaml.Unmarshal(data, &c))
	delete(c.Backends, "moqb")
	out, err := yaml.Marshal(&c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.ConfigPath, out, 0o600))
	sighupUntilReloaded(t, h.MetricsAddr)

	// the session keeps the backend a reload removed, and its subscription keeps streaming
	origin.publish(t, "/b", 1, "after")
	require.Equal(t, "after", moqNext(t, r.objects, "an object after the reload"))
	require.NoError(t, s.Err())

	// the reload swaps the routes once it completes, after which a new session for the removed backend is refused
	require.Eventually(t, func() bool {
		ns, err := dialMoQ(t, addr, "b.example.com")
		if err == nil {
			select {
			case <-ns.Done():
				err = ns.Err()
			case <-time.After(100 * time.Millisecond):
				_ = ns.Close(message.CodeNoError, "")
				return false
			}
		}
		var ce *session.CloseError
		return errors.As(err, &ce) && ce.Code == uint64(message.CodeInvalidPath)
	}, moqWait, 100*time.Millisecond, "a new session for the removed backend is refused")
	origin.publish(t, "/b", 2, "still")
	require.Equal(t, "still", moqNext(t, r.objects, "an object once the reload completed"))
	_, err = dialMoQ(t, addr, "a.example.com")
	require.NoError(t, err, "the backends the reload kept serve new sessions")
}
