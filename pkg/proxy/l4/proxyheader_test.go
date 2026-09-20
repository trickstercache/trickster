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

package l4

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// headerListener accepts connections that arrived behind a PROXY protocol header, as the
// daemon's listener hands them over
type headerListener struct{ net.Listener }

type headerConn struct{ net.Conn }

func (l headerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return headerConn{c}, nil
}

func (headerConn) ProxyTLV(typ byte) ([]byte, bool) {
	return []byte{typ, typ}, typ == 0xEA
}

func TestFlowCarriesTheProxyHeader(t *testing.T) {
	cert := selfSigned(t, "shop.example.com")
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	for protocol, upstreamTLS := range map[string]*tls.Config{ProtocolTCP: nil, ProtocolTLS: tlsConf} {
		up := rotate(echoServer(t, "echo:", upstreamTLS))
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := NewServer("test", protocol, &Config{
			Table:   tableOf(t, map[string]Upstream{"": up}),
			Options: &options.Options{ConnectTimeout: timeconv.Duration(time.Second)},
		})
		go func() { _ = srv.Serve(headerListener{ln}) }()
		t.Cleanup(func() { _ = srv.Close() })
		conn := dialTCP(t, ln.Addr().String())
		if upstreamTLS != nil {
			conn = tls.Client(conn, &tls.Config{ServerName: "shop.example.com", InsecureSkipVerify: true}) // #nosec G402
		}
		if got := exchange(t, conn, "hi"); got != "echo:hi" {
			t.Fatalf("%s: reply = %q", protocol, got)
		}
		up.mu.Lock()
		flow := up.flows[0]
		up.mu.Unlock()
		if flow.Proxy == nil {
			t.Fatalf("%s: the flow lost the connection's PROXY header", protocol)
		}
		if v, ok := flow.Proxy.ProxyTLV(0xEA); !ok || len(v) != 2 {
			t.Errorf("%s: TLV = %v, %v", protocol, v, ok)
		}
	}
	// a plain connection has no header to offer
	up := rotate(echoServer(t, "echo:", nil))
	_, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	exchange(t, dialTCP(t, addr), "hi")
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.flows[0].Proxy != nil {
		t.Error("a connection without a PROXY header produced one")
	}
}
