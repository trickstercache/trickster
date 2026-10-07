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
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"

	"github.com/pires/go-proxyproto"
)

func TestStreamConfigAdmission(t *testing.T) {
	client := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 4242)
	other := netip.AddrPortFrom(netip.MustParseAddr("192.0.2.9"), 4242)

	t.Run("tcp peer", func(t *testing.T) {
		conf, clients := streamConfigFor(t, 1, listenerconfig.ProtocolTCP, "127.0.0.1:9")
		conf.Listeners["relay"].IPACL = mustList(t, ipacl.Options{Source: "peer", Allow: []string{"10.1.1.1"}})
		cfg := streamConfig(conf, streamDesired(conf), clients)
		if cfg.Admission != nil {
			t.Fatal("a tcp peer list was installed as admission")
		}
	})

	t.Run("tcp backend", func(t *testing.T) {
		conf, clients := streamConfigFor(t, 1, listenerconfig.ProtocolTCP, "127.0.0.1:9")
		conf.Backends["db"].IPACL = mustList(t, ipacl.Options{Action: "reject"})
		cfg := streamConfig(conf, streamDesired(conf), clients)
		if cfg.Admission == nil || cfg.Admission.Datagrams() {
			t.Fatal("the backend list was not an admission that leaves datagrams alone")
		}
		if _, ok := cfg.Admission.(l4.Holder); ok {
			t.Fatal("the admission installs its own udp hold")
		}
		if got := cfg.Admission.Peer(l4.Flow{Client: client}); got != l4.Allow {
			t.Fatalf("peer = %v", got)
		}
		if got := cfg.Admission.Flow(l4.Flow{Client: client}); got != l4.Reject {
			t.Fatalf("flow = %v", got)
		}
	})

	t.Run("udp", func(t *testing.T) {
		conf, clients := streamConfigFor(t, 1, listenerconfig.ProtocolUDP, "127.0.0.1:9")
		conf.Listeners["relay"].IPACL = mustList(t, ipacl.Options{Source: "peer", Allow: []string{"127.0.0.1"}})
		conf.Backends["db"].IPACL = mustList(t, ipacl.Options{Action: "drop", Allow: []string{"127.0.0.1"}})
		cfg := streamConfig(conf, streamDesired(conf), clients)
		if cfg.Admission == nil || cfg.Admission.Datagrams() {
			t.Fatal("udp lists were not an admission that leaves datagrams alone")
		}
		if got := cfg.Admission.Peer(l4.Flow{Client: client}); got != l4.Allow {
			t.Fatalf("allowed peer = %v", got)
		}
		if got := cfg.Admission.Peer(l4.Flow{Client: other}); got != l4.Reject {
			t.Fatalf("denied peer = %v, want the listener reject", got)
		}
	})

	t.Run("tls route", func(t *testing.T) {
		conf, clients := streamConfigFor(t, 1, listenerconfig.ProtocolTLS, "127.0.0.1:1")
		conf.Listeners["relay"].IPACL = mustList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
		conf.Backends["db"].Hosts = []string{"shop.example.com"}
		conf.Backends["db"].IPACL = mustList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
		addStreamBackend(t, conf, clients, "wild", "127.0.0.1:2", []string{"*.example.com"},
			mustList(t, ipacl.Options{Action: "drop"}))
		cfg := streamConfig(conf, streamDesired(conf), clients)
		if cfg.Admission.Peer(l4.Flow{Client: client}) != l4.Allow {
			t.Fatal("the listener list denied the client")
		}
		if cfg.Admission.Peer(l4.Flow{Client: other}) != l4.Reject {
			t.Fatal("the listener list allowed a denied client")
		}
		for _, name := range []string{"shop.example.com", "Shop.Example.com"} {
			if got := cfg.Admission.Flow(l4.Flow{Client: client, ServerName: name}); got != l4.Allow {
				t.Fatalf("%s = %v", name, got)
			}
		}
		if got := cfg.Admission.Flow(l4.Flow{Client: client, ServerName: "other.example.com"}); got != l4.Drop {
			t.Fatalf("wildcard = %v", got)
		}
		// one label of depth: this name is not the wildcard, and nothing else routes it
		if got := cfg.Admission.Flow(l4.Flow{Client: client, ServerName: "a.b.example.com"}); got != l4.Allow {
			t.Fatalf("deeper name = %v", got)
		}
	})
}

func streamDesired(conf *config.Config) desiredListener {
	return desiredListener{listenerName: "relay", options: conf.Listeners["relay"]}
}

func addStreamBackend(t *testing.T, conf *config.Config, clients backends.Backends, name, origin string, hosts []string, list *ipacl.List) {
	t.Helper()
	o := bo.New()
	o.Provider = providers.ReverseProxyShort
	o.OriginURL = "tcp://" + origin
	o.ListenerNames = []string{"relay"}
	o.Hosts = hosts
	if err := o.Initialize(name); err != nil {
		t.Fatal(err)
	}
	o.IPACL = list
	conf.Backends[name] = o
	client, err := backends.New(name, o, nil, lm.NewRouter(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clients[name] = client
}

func TestStreamClientIPReloadsWithoutRestart(t *testing.T) {
	echo := lineEcho(t, "echo:")
	port := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	conf, clients := streamConfigFor(t, port, listenerconfig.ProtocolTCP, echo)
	conf.Listeners["relay"].IPACL = mustList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
	applyStream(t, conf, nil, clients, group)
	key := listenerKey("relay", listenerconfig.ProtocolTCP, false)
	waitForListener(t, group, key)
	original := group.Get(key)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if got := streamLine(t, conn, reader, "hello"); got != "echo:hello" {
		t.Fatalf("reply = %q", got)
	}

	next, nextClients := streamConfigFor(t, port, listenerconfig.ProtocolTCP, echo)
	next.Listeners["relay"].IPACL = mustList(t, ipacl.Options{Action: "reject"})
	applyStream(t, next, conf, nextClients, group)
	if group.Get(key) != original {
		t.Fatal("an access-list change restarted the stream listener")
	}
	if got := streamLine(t, conn, reader, "again"); got != "echo:again" {
		t.Fatalf("established reply = %q", got)
	}
	expectStreamReset(t, port)
}

func TestStreamPROXYPeerAndClientIP(t *testing.T) {
	t.Run("peer judges the socket", func(t *testing.T) {
		echo, dials := countingLineEcho(t, "echo:")
		port := availablePort(t)
		group := listener.NewGroup()
		t.Cleanup(func() { _ = group.Shutdown(0) })
		// 192.0.2.9 would pass this list. The socket is 127.0.0.1, and accept resets it.
		conf, clients := proxyStream(t, port, echo, ipacl.Options{Source: "peer", Allow: []string{"192.0.2.9"}})
		applyStream(t, conf, nil, clients, group)
		waitForListener(t, group, listenerKey("relay", listenerconfig.ProtocolTCP, false))
		expectStreamReset(t, port)
		expectNoDials(t, dials)
	})

	t.Run("peer ignores the header source", func(t *testing.T) {
		echo, _ := countingLineEcho(t, "echo:")
		port := availablePort(t)
		group := listener.NewGroup()
		t.Cleanup(func() { _ = group.Shutdown(0) })
		// the socket is allowed; the header source is not, and a backend list keeps admission installed
		conf, clients := proxyStream(t, port, echo, ipacl.Options{Source: "peer", Allow: []string{"127.0.0.1"}})
		conf.Backends["db"].IPACL = mustList(t, ipacl.Options{Default: "allow"})
		applyStream(t, conf, nil, clients, group)
		waitForListener(t, group, listenerKey("relay", listenerconfig.ProtocolTCP, false))
		conn := dialStreamProxy(t, port, "192.0.2.9")
		defer conn.Close()
		if got := streamLine(t, conn, bufio.NewReader(conn), "hi"); got != "echo:hi" {
			t.Fatalf("reply = %q", got)
		}
	})

	t.Run("client_ip judges the header source", func(t *testing.T) {
		echo, dials := countingLineEcho(t, "echo:")
		port := availablePort(t)
		group := listener.NewGroup()
		t.Cleanup(func() { _ = group.Shutdown(0) })
		conf, clients := proxyStream(t, port, echo, ipacl.Options{Allow: []string{"192.0.2.9"}})
		applyStream(t, conf, nil, clients, group)
		waitForListener(t, group, listenerKey("relay", listenerconfig.ProtocolTCP, false))
		conn := dialStreamProxy(t, port, "192.0.2.9")
		defer conn.Close()
		if got := streamLine(t, conn, bufio.NewReader(conn), "hi"); got != "echo:hi" {
			t.Fatalf("reply = %q", got)
		}
		denied := dialStreamProxy(t, port, "198.51.100.8")
		defer denied.Close()
		if err := readStreamReset(t, denied); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		if got := dials.Load(); got != 1 {
			t.Fatalf("upstream accepted %d connections, want the allowed header source", got)
		}
	})
}

func proxyStream(t *testing.T, port int, origin string, list ipacl.Options) (*config.Config, backends.Backends) {
	t.Helper()
	conf, clients := streamConfigFor(t, port, listenerconfig.ProtocolTCP, origin)
	lo := conf.Listeners["relay"]
	lo.ProxyProtocol = true
	lo.TrustedProxies = []string{"127.0.0.1/32"}
	lo.IPACL = mustList(t, list)
	return conf, clients
}

func applyStream(t *testing.T, conf, old *config.Config, clients backends.Backends, group *listener.Group) {
	t.Helper()
	applyListenerConfigs(conf, old, nil, http.NotFoundHandler(), lm.NewRouter(), nil, clients, nil, group)
}

func countingLineEcho(t *testing.T, prefix string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if line != "" {
						_, _ = conn.Write([]byte(prefix + line))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), &n
}

func streamLine(t *testing.T, conn net.Conn, reader *bufio.Reader, line string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	reply, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(reply)
}

func dialStreamProxy(t *testing.T, port int, src string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h := proxyproto.HeaderProxyFromAddrs(2,
		&net.TCPAddr{IP: net.ParseIP(src), Port: 4242},
		&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80})
	if _, err := h.WriteTo(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func expectStreamReset(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if errors.Is(err, syscall.ECONNRESET) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := readStreamReset(t, conn); err != nil {
		t.Fatal(err)
	}
}

func readStreamReset(t *testing.T, conn net.Conn) error {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(make([]byte, 1))
	if n != 0 {
		return fmt.Errorf("the server wrote %d bytes", n)
	}
	if !errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("read %v, want a reset", err)
	}
	return nil
}

func TestStreamConfigRateLimit(t *testing.T) {
	tcpOpts := &rlopts.Options{Name: "cfg-tcp", Limit: 1, Unit: rlopts.UnitConnections}
	if err := tcpOpts.Validate(); err != nil {
		t.Fatal(err)
	}
	conf, clients := streamConfigFor(t, 1, listenerconfig.ProtocolTCP, "127.0.0.1:9")
	conf.Listeners["relay"].RateLimiterName = tcpOpts.Name
	conf.Listeners["relay"].RateLimiter = tcpOpts
	cfg := streamConfig(conf, streamDesired(conf), clients)
	if _, ok := cfg.Admission.(l4.Holder); ok {
		t.Fatal("tcp connections installed a udp hold")
	}

	udpOpts := &rlopts.Options{Name: "cfg-udp", Limit: 1, Unit: rlopts.UnitSessions}
	if err := udpOpts.Validate(); err != nil {
		t.Fatal(err)
	}
	conf, clients = streamConfigFor(t, 1, listenerconfig.ProtocolUDP, "127.0.0.1:9")
	conf.Listeners["relay"].RateLimiterName = udpOpts.Name
	conf.Listeners["relay"].RateLimiter = udpOpts
	cfg = streamConfig(conf, streamDesired(conf), clients)
	if _, ok := cfg.Admission.(l4.Holder); !ok {
		t.Fatal("udp sessions have no hold")
	}

	deferOpts := &rlopts.Options{Name: "cfg-defer", Limit: 2, Unit: rlopts.UnitConnections}
	if err := deferOpts.Validate(); err != nil {
		t.Fatal(err)
	}
	conf, clients = streamConfigFor(t, 1, listenerconfig.ProtocolTCP, "127.0.0.1:9")
	conf.Backends["db"].IPACL = mustList(t, ipacl.Options{Action: "reject"})
	conf.Listeners["relay"].RateLimiterName = deferOpts.Name
	conf.Listeners["relay"].RateLimiter = deferOpts
	cfg = streamConfig(conf, streamDesired(conf), clients)
	client := netip.AddrPortFrom(netip.MustParseAddr("192.0.2.9"), 1)
	if cfg.Admission.Peer(l4.Flow{Client: client}) != l4.Allow {
		t.Fatal("peer")
	}
	if cfg.Admission.Flow(l4.Flow{Client: client}) != l4.Reject {
		t.Fatal("the backend list did not refuse before the limiter")
	}
	var n int
	ratelimit.Walk(func(name string, keys int) {
		if name == "cfg-defer" {
			n = keys
		}
	})
	if n != 0 {
		t.Fatalf("flow admission charged %d buckets", n)
	}
}

func expectNoDials(t *testing.T, n *atomic.Int32) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if got := n.Load(); got != 0 {
		t.Fatalf("upstream accepted %d connections", got)
	}
}
