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

package setup

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	geoacl "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func gateBackend(t *testing.T, o *bo.Options, aclName string) {
	t.Helper()
	// the geofeed places the loopback address in France, which the geo ACL denies
	feed, err := geofeed.New(aclName, &geofeedopts.Options{Entries: []string{"127.0.0.1/32,FR"}})
	require.NoError(t, err)
	opts := &geoaclopts.Options{Name: aclName, Deny: []string{"FR"}}
	a, err := geoacl.Compile(opts, feed, aclName)
	require.NoError(t, err)
	opts.Compiled = a
	o.GeoACLName, o.GeoACLOptions = aclName, opts
}

func streamDenials(aclName string) float64 {
	return testutil.ToFloat64(metrics.GeoACLDecisions.WithLabelValues(aclName, geoacl.PlaneStream.String(),
		geoacl.ResultDenied.String()))
}

func TestStreamGeoACLTCP(t *testing.T) {
	const aclName = "stream-geo-tcp"
	var accepted atomic.Int32
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = origin.Close() })
	go func() {
		for {
			c, err := origin.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	port := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf, clients := streamConfigFor(t, port, listenerconfig.ProtocolTCP, origin.Addr().String())
	gateBackend(t, conf.Backends["db"], aclName)
	applyListenerConfigs(conf, nil, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients, nil, group)
	waitForListener(t, group, listenerKey("relay", listenerconfig.ProtocolTCP, false))

	// a refused client sees a reset, which can beat the dial's return, and nothing is dialed for it
	before := streamDenials(aclName)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = conn.Read(make([]byte, 1))
		_ = conn.Close()
	}
	require.True(t, errors.Is(err, syscall.ECONNRESET), "expected a reset, got %v", err)
	require.Equal(t, before+1, streamDenials(aclName))
	require.Zero(t, accepted.Load(), "a refused client was relayed")

	// a reload that removes the geo ACL lets the next connection through
	open, clients2 := streamConfigFor(t, port, listenerconfig.ProtocolTCP, origin.Addr().String())
	applyListenerConfigs(open, conf, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients2, nil, group)
	conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.Eventually(t, func() bool { return accepted.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
}

func TestStreamGeoACLUDP(t *testing.T) {
	const aclName = "stream-geo-udp"
	var received atomic.Int32
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			received.Add(1)
		}
	}()
	port := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf, clients := streamConfigFor(t, port, listenerconfig.ProtocolUDP, pc.LocalAddr().String())
	gateBackend(t, conf.Backends["db"], aclName)
	applyListenerConfigs(conf, nil, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients, nil, group)
	waitForListener(t, group, listenerKey("relay", listenerconfig.ProtocolUDP, false))

	// a refused client's datagram opens no flow and dials nothing
	before := streamDenials(aclName)
	client, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer client.Close()
	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return streamDenials(aclName) == before+1 }, 3*time.Second,
		10*time.Millisecond)
	require.Zero(t, received.Load())
}

func TestStreamGeoACLTLSByServerName(t *testing.T) {
	conf, clients := streamConfigFor(t, availablePort(t), listenerconfig.ProtocolTLS, "127.0.0.1:1")
	gated := conf.Backends["db"]
	gated.Hosts = []string{"*.example.com"}
	gateBackend(t, gated, "stream-geo-tls")
	open := bo.New()
	open.Hosts = []string{"api.example.com", ""}
	open.ListenerNames = []string{"relay"}
	conf.Backends["open"] = open
	clients["open"] = clients["db"]
	cfg := streamConfig(conf, desiredListener{listenerName: "relay", options: conf.Listeners["relay"]}, clients)
	require.NotNil(t, cfg.Admission)
	require.False(t, cfg.Admission.Datagrams())
	client := netip.MustParseAddrPort("127.0.0.1:4000")
	require.Equal(t, l4.Allow, cfg.Admission.Peer(l4.Flow{Client: client}), "tls is judged by server name")
	for name, want := range map[string]l4.Verdict{
		"www.example.com": l4.Reject,
		"api.example.com": l4.Allow, // the exact host of an ungated backend beats a gated wildcard
		"other.test":      l4.Allow, // the ungated catch-all
	} {
		require.Equal(t, want, cfg.Admission.Flow(l4.Flow{Client: client, ServerName: name}), name)
	}

	// a listener whose backends name no geo ACL has no admission
	conf.Backends["db"].GeoACLOptions = nil
	cfg = streamConfig(conf, desiredListener{listenerName: "relay", options: conf.Listeners["relay"]}, clients)
	require.Nil(t, cfg.Admission)
}

func TestStreamConfigChainsIPAndGeoACLs(t *testing.T) {
	const aclName = "stream-geo-and-ip"
	client := l4.Flow{Client: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 4242)}
	conf, clients := streamConfigFor(t, 1, listenerconfig.ProtocolTCP, "127.0.0.1:9")
	gateBackend(t, conf.Backends["db"], aclName)

	// an IP list that allows the client leaves the refusal to the geo ACL
	conf.Backends["db"].IPACL = mustList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
	before := streamDenials(aclName)
	cfg := streamConfig(conf, streamDesired(conf), clients)
	require.Equal(t, l4.Reject, cfg.Admission.Peer(client))
	require.Equal(t, before+1, streamDenials(aclName))

	// a listener IP list that refuses the client decides first, and the geo ACL is never asked
	conf.Listeners["relay"].IPACL = mustList(t, ipacl.Options{Action: "drop"})
	cfg = streamConfig(conf, streamDesired(conf), clients)
	require.Equal(t, l4.Drop, cfg.Admission.Peer(client))
	require.Equal(t, before+1, streamDenials(aclName))
}
