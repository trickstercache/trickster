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
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
	"github.com/trickstercache/trickster/v2/pkg/proxy/quicdial"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

const (
	ingestBackend    = "studio"
	ingestPubToken   = "studio-publisher"
	ingestSubToken   = "studio-viewer"
	ingestLateToken  = "late-viewer"
	ingestTokenGrant = "%s publish live\n%s subscribe live\n"
)

type ingestHarness struct {
	tricksterHarness
	moqAddr   string
	h3Addr    string
	pool      *x509.CertPool
	tokenPath string
}

func moqIngestHarness(t *testing.T) ingestHarness {
	t.Helper()
	// an origin-less backend accepts publishers on a moq listener and by WebTransport through the default
	// listener, authorizing requests by a token file
	serve, pool := moqServingTLS(t)
	tokenPath := filepath.Join(t.TempDir(), "tokens")
	require.NoError(t, os.WriteFile(tokenPath, fmt.Appendf(nil, ingestTokenGrant, ingestPubToken, ingestSubToken), 0o600))
	tlsPorts, releaseTLS := portutil.Reserve(t, 1)
	udpPorts, releaseUDP := portutil.ReserveUDP(t, 2)
	h := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "127.0.0.1", ListenPort: udpPorts[0],
		}
		l := c.Listeners[listenerconfig.DefaultFrontendName]
		l.TLSListenAddress, l.TLSListenPort = "127.0.0.1", tlsPorts[0]
		l.HTTP3 = &listenerconfig.HTTP3Options{Enabled: true, ListenAddress: "127.0.0.1", ListenPort: udpPorts[1]}
		b := bo.New()
		b.Name, b.Provider, b.CacheName = ingestBackend, providers.MoQ, "default"
		b.ListenerNames = []string{moqListener, listenerconfig.DefaultFrontendName}
		b.TLS = serve
		b.MoQ = mo.New()
		b.MoQ.Publish.Enabled = true
		b.MoQ.Authorization.Mode, b.MoQ.Authorization.TokenFile = mo.AuthorizationToken, tokenPath
		c.Backends[ingestBackend] = b
	})
	releaseTLS()
	releaseUDP()
	h.start(t)
	return ingestHarness{
		tricksterHarness: h, moqAddr: fmt.Sprintf("127.0.0.1:%d", udpPorts[0]),
		h3Addr: fmt.Sprintf("127.0.0.1:%d", udpPorts[1]), pool: pool, tokenPath: tokenPath,
	}
}

func setupTokens(values ...string) []wire.Token {
	out := make([]wire.Token, len(values))
	for i, v := range values {
		out[i] = wire.Token{AliasType: wire.TokenUseValue, Value: []byte(v)}
	}
	return out
}

func (h ingestHarness) native(t *testing.T, tokens ...string) *session.Session {
	t.Helper()
	// a session on the moq listener, its SETUP carrying tokens
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	conn, err := quicdial.DialAddr(ctx, h.moqAddr, &tls.Config{
		RootCAs: h.pool, ServerName: "localhost",
		NextProtos: codec.ALPNs(codec.ClientOffer), MinVersion: tls.VersionTLS13,
	},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	require.NoError(t, err)
	c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	s, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c,
		Setup: message.Setup{
			Path: "/", HasPath: true, Authority: "localhost", HasAuthority: true,
			AuthTokens: setupTokens(tokens...),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	require.NoError(t, s.Start(ctx))
	return s
}

func (h ingestHarness) browser(t *testing.T, tokens ...string) *session.Session {
	t.Helper()
	// a session by WebTransport through the default listener, its SETUP carrying tokens
	tr := &webtransport.Transport{DialAddr: quicdial.DialAddrEarly,
		TLSClientConfig:      &tls.Config{RootCAs: h.pool, ServerName: "localhost", MinVersion: tls.VersionTLS13},
		ApplicationProtocols: codec.ALPNs(codec.ClientOffer),
	}
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	_, ws, err := tr.Dial(ctx, "https://"+h.h3Addr+"/"+ingestBackend+"/live", nil)
	require.NoError(t, err)
	c, ok := codec.ForALPN(ws.SessionState().ApplicationProtocol)
	require.True(t, ok)
	s, err := session.New(session.FromWebTransport(ws), session.Options{
		Role: session.RoleClient, Codec: c, WebTransport: true,
		Setup: message.Setup{AuthTokens: setupTokens(tokens...)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	require.NoError(t, s.Start(ctx))
	return s
}

func moqResponse(t *testing.T, req *session.Request) message.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	m, err := req.Response(ctx)
	require.NoError(t, err)
	return m
}

func publishGroup(t *testing.T, req *session.Request, group uint64, payloads ...string) {
	t.Helper()
	out, err := req.OpenSubgroup(context.Background(),
		message.SubgroupHeader{Group: group, Priority: 1, EndOfGroup: true, FirstObject: true}, 3, false)
	require.NoError(t, err)
	for i, p := range payloads {
		require.NoError(t, out.WriteObject(&message.SubgroupObject{ID: uint64(i), PayloadLen: uint64(len(p))}))
		_, err = out.Write([]byte(p))
		require.NoError(t, err)
	}
	require.NoError(t, out.Close())
}

func TestMoQIngest(t *testing.T) {
	h := moqIngestHarness(t)

	// a publisher on the native listener and a subscriber on the WebTransport one meet at the backend's relay
	publisher := h.native(t, ingestPubToken)
	pub, err := publisher.Open(context.Background(), &message.Publish{
		Track: moqTrackName(), TrackAlias: publisher.NewTrackAlias(),
	}, moqNop{}, nil)
	require.NoError(t, err)
	require.Equal(t, message.KindRequestOK, moqResponse(t, pub).Kind(), "the publisher is authorized")
	viewer := h.browser(t, ingestSubToken)
	r := moqSubscribe(t, viewer)
	publishGroup(t, pub, 0, "live-key", "live-delta")
	require.Equal(t, "live-key", moqNext(t, r.objects, "a key object"))
	require.Equal(t, "live-delta", moqNext(t, r.objects, "a delta object"))
	publishers, found := metricValue(t, h.MetricsAddr, "trickster_moq_publishers_active",
		`backend_name="studio"`)
	require.True(t, found)
	require.Equal(t, 1.0, publishers)

	// requests without a granted token are refused
	anon, err := h.browser(t).Open(context.Background(), &message.Subscribe{Track: moqTrackName()}, moqNop{}, nil)
	require.NoError(t, err)
	re, ok := moqResponse(t, anon).(*message.RequestError)
	require.True(t, ok)
	require.Equal(t, message.RequestUnauthorized, re.Code)

	// a token added to the file is honored without a reload
	require.NoError(t, os.WriteFile(h.tokenPath, fmt.Appendf(nil, ingestTokenGrant+"%s subscribe live\n",
		ingestPubToken, ingestSubToken, ingestLateToken), 0o600))
	late := h.native(t, ingestLateToken)
	require.Eventually(t, func() bool {
		req, err := late.Open(context.Background(), &message.TrackStatus{Track: moqTrackName()}, moqNop{}, nil)
		if err != nil {
			return false
		}
		_, ok := moqResponse(t, req).(*message.RequestOK)
		return ok
	}, moqWait, 100*time.Millisecond, "the late token is granted once the file is read again")

	// a published namespace is reported to a namespace subscription, within what each token grants
	studio := wire.Namespace{[]byte("live"), []byte("studio-a")}
	ann, err := publisher.Open(context.Background(), &message.PublishNamespace{Prefix: studio}, moqNop{}, nil)
	require.NoError(t, err)
	require.Equal(t, message.KindRequestOK, moqResponse(t, ann).Kind())
	everything, err := viewer.Open(context.Background(), &message.SubscribeNamespace{}, moqNop{}, nil)
	require.NoError(t, err)
	re, ok = moqResponse(t, everything).(*message.RequestError)
	require.True(t, ok, "a token for live does not cover every namespace")
	require.Equal(t, message.RequestUnauthorized, re.Code)
	watch := &namespaceWatch{found: make(chan wire.Namespace, 4)}
	ns, err := viewer.Open(context.Background(), &message.SubscribeNamespace{Prefix: studio[:1]}, watch, nil)
	require.NoError(t, err)
	require.Equal(t, message.KindRequestOK, moqResponse(t, ns).Kind())
	select {
	case got := <-watch.found:
		require.True(t, got.Equal(studio[1:]), "NAMESPACE carries the suffix after the subscribed prefix")
	case <-time.After(moqWait):
		t.Fatal("no NAMESPACE arrived")
	}
}

type namespaceWatch struct {
	found chan wire.Namespace
}

func (w *namespaceWatch) HandleMessage(_ *session.Request, m message.Message) {
	if n, ok := m.(*message.Namespace); ok {
		w.found <- n.Suffix
	}
}

func (w *namespaceWatch) HandleClose(*session.Request, error) {}
