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
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

const (
	moqEdgeBackend = "edge"
	moqOriginsALB  = "origins"
)

func moqALBHarness(t *testing.T, origins ...*moqOrigin) (tricksterHarness, string) {
	t.Helper()
	// one moq backend per origin, an alb of them, and the edge backend on the moq listener that reaches its origins
	// through the alb, re-subscribing quickly after a loss
	serve, _ := moqServingTLS(t)
	serve.InsecureSkipVerify = true
	udp, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "127.0.0.1", ListenPort: udp[0],
		}
		var members []string
		for i, o := range origins {
			b := bo.New()
			b.Name, b.Provider, b.CacheName = fmt.Sprintf("origin%d", i), providers.MoQ, "default"
			b.OriginURL, b.TLS, b.MoQ = "moqt://"+o.addr+"/live", serve, mo.New()
			c.Backends[b.Name] = b
			members = append(members, b.Name)
		}
		c.Backends[moqOriginsALB] = &bo.Options{
			Name: moqOriginsALB, Provider: providers.ALB,
			ALBOptions: &ao.Options{MechanismName: "rr", Pool: ao.Members(members...)},
		}
		edge := bo.New()
		edge.Name, edge.Provider, edge.CacheName = moqEdgeBackend, providers.MoQ, "default"
		edge.ListenerNames, edge.TLS, edge.MoQ = []string{moqListener}, serve, mo.New()
		edge.MoQ.Upstream.ALB = moqOriginsALB
		edge.MoQ.Upstream.ReconnectBackoff = timeconv.Duration(50 * time.Millisecond)
		c.Backends[moqEdgeBackend] = edge
	})
	releaseUDP()
	h.start(t)
	return h, fmt.Sprintf("127.0.0.1:%d", udp[0])
}

func TestMoQALBFailover(t *testing.T) {
	a, b := startMoQOrigin(t), startMoQOrigin(t)
	_, addr := moqALBHarness(t, a, b)
	s, err := dialMoQ(t, addr, "localhost")
	require.NoError(t, err)
	r := moqSubscribe(t, s)
	first, second := a, b
	require.Eventually(t, func() bool { return a.subscribes.Load()+b.subscribes.Load() == 1 }, moqWait,
		10*time.Millisecond, "the edge subscribes to one origin")
	if b.subscribes.Load() == 1 {
		first, second = b, a
	}
	first.publish(t, "/live", 0, "from-first")
	require.Equal(t, "from-first", moqNext(t, r.objects, "an object from the first origin"))

	// the origin serving the track goes away; the edge re-subscribes to the other one, and the subscriber never knows
	first.kill()
	require.Eventually(t, func() bool { return second.subscribes.Load() == 1 }, moqWait, 10*time.Millisecond,
		"the edge fails over to the other origin")
	require.Eventually(t, func() bool {
		second.mu.Lock()
		defer second.mu.Unlock()
		return len(second.subs["/live"]) == 1
	}, moqWait, 10*time.Millisecond)
	second.publish(t, "/live", 1, "from-second")
	require.Equal(t, "from-second", moqNext(t, r.objects, "an object from the second origin"))
	require.NoError(t, s.Err(), "the subscriber's session is untouched")
}

type moqMigration struct {
	moqNop
	goAway chan *message.GoAway
}

func (m *moqMigration) HandleRequest(r *session.Request) {
	_ = r.Reject(message.RequestNotSupported, "", 0)
}

func (m *moqMigration) HandleGoAway(_ *session.Session, g *message.GoAway) {
	m.goAway <- g
}

func TestMoQShutdownGoAway(t *testing.T) {
	origin := startMoQOrigin(t)
	alternate := "moqt://" + origin.addr + "/live"
	serve, _ := moqServingTLS(t)
	serve.InsecureSkipVerify = true
	udp, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		lo := mo.NewListener()
		// the origin stands in for the relay that takes over the sessions
		lo.GoAwayURI = alternate
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "127.0.0.1", ListenPort: udp[0], MoQ: lo,
		}
		b := bo.New()
		b.Name, b.Provider, b.CacheName = "moqlive", providers.MoQ, "default"
		b.OriginURL, b.ListenerNames, b.TLS, b.MoQ = alternate, []string{moqListener}, serve, mo.New()
		c.Backends[b.Name] = b
	})
	releaseUDP()
	stop := h.startStoppable(t)

	m := &moqMigration{goAway: make(chan *message.GoAway, 1)}
	old := dialMoQHandler(t, fmt.Sprintf("127.0.0.1:%d", udp[0]), "localhost", "/", m)
	r := moqSubscribe(t, old)
	origin.publish(t, "/live", 0, "before")
	require.Equal(t, "before", moqNext(t, r.objects, "an object before the shutdown"))

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	var g *message.GoAway
	select {
	case g = <-m.goAway:
	case <-time.After(moqWait):
		t.Fatal("no GOAWAY as Trickster stopped")
	}
	require.Equal(t, alternate, g.NewSessionURI)
	require.Positive(t, g.Timeout, "the GOAWAY says how long the session has to move")

	// the client follows the GOAWAY to the alternate and carries on there, then lets the old session go
	u, err := url.Parse(g.NewSessionURI)
	require.NoError(t, err)
	moved := dialMoQHandler(t, u.Host, "localhost", u.Path, &moqMigration{goAway: make(chan *message.GoAway, 1)})
	nr := moqSubscribe(t, moved)
	require.NoError(t, old.Close(message.CodeNoError, ""))
	origin.publish(t, "/live", 1, "after")
	require.Equal(t, "after", moqNext(t, nr.objects, "an object on the new session"))
	select {
	case <-stopped:
	case <-time.After(moqWait):
		t.Fatal("Trickster did not finish stopping once its only session moved")
	}
}

func dialMoQHandler(t *testing.T, addr, authority, path string, h session.Handler) *session.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, // #nosec G402 -- test certificate
		NextProtos: codec.ALPNs(codec.ClientOffer), MinVersion: tls.VersionTLS13},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	require.NoError(t, err)
	c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	s, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c, Handler: h,
		Setup: message.Setup{Path: path, HasPath: true, Authority: authority, HasAuthority: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	require.NoError(t, s.Start(ctx))
	return s
}
