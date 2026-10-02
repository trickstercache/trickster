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

package listener

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"

	"github.com/pires/go-proxyproto"
	"github.com/stretchr/testify/require"
)

func TestNewProxyProtocolOptions(t *testing.T) {
	require.Nil(t, NewProxyProtocolOptions(false, nil))
	trusted, err := clientip.ParseTrusted([]string{"10.0.0.0/8"})
	require.NoError(t, err)
	o := NewProxyProtocolOptions(true, trusted)
	require.True(t, o.Enabled)
	require.Equal(t, trusted, o.Trusted)

	// every peer is honored without a trusted list; otherwise only members
	p, err := (&ProxyProtocolOptions{Enabled: true}).policy(proxyproto.ConnPolicyOptions{
		Upstream: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1},
	})
	require.NoError(t, err)
	require.Equal(t, proxyproto.USE, p)
	p, _ = o.policy(proxyproto.ConnPolicyOptions{Upstream: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 1}})
	require.Equal(t, proxyproto.USE, p)
	p, _ = o.policy(proxyproto.ConnPolicyOptions{Upstream: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1}})
	require.Equal(t, proxyproto.SKIP, p)
	p, _ = o.policy(proxyproto.ConnPolicyOptions{Upstream: &net.UnixAddr{Name: "sock", Net: "unix"}})
	require.Equal(t, proxyproto.SKIP, p)
}

func proxyRoundTrip(t *testing.T, addr string, header string) (string, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = io.WriteString(conn, header+"GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.Status, strings.TrimSpace(string(body))
}

func TestListenerProxyProtocol(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.RemoteAddr)
	})
	start := func(t *testing.T, o *ProxyProtocolOptions) string {
		t.Helper()
		lg := NewGroup()
		name := t.Name()
		go func() { _ = lg.StartListener(name, "127.0.0.1", 0, 0, nil, echo, nil, nil, time.Second, o) }()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if l := lg.Get(name); l != nil && l.State() == StateReady {
				t.Cleanup(func() { _ = lg.DrainAndClose(name, time.Second) })
				return l.Addr().String()
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("listener did not start")
		return ""
	}
	const v1 = "PROXY TCP4 203.0.113.9 10.0.0.1 4242 80\r\n"

	t.Run("header honored from any peer", func(t *testing.T) {
		addr := start(t, &ProxyProtocolOptions{Enabled: true})
		status, body := proxyRoundTrip(t, addr, v1)
		require.Equal(t, "200 OK", status)
		require.Equal(t, "203.0.113.9:4242", body)
		// a connection without a header is served with its own address
		_, body = proxyRoundTrip(t, addr, "")
		require.True(t, strings.HasPrefix(body, "127.0.0.1:"), body)
	})

	t.Run("header skipped from an untrusted peer", func(t *testing.T) {
		trusted, err := clientip.ParseTrusted([]string{"10.0.0.0/8"})
		require.NoError(t, err)
		addr := start(t, &ProxyProtocolOptions{Enabled: true, Trusted: trusted})
		status, _ := proxyRoundTrip(t, addr, v1)
		require.Equal(t, "400 Bad Request", status)
		_, body := proxyRoundTrip(t, addr, "")
		require.True(t, strings.HasPrefix(body, "127.0.0.1:"), body)
	})

	t.Run("header honored from a trusted peer", func(t *testing.T) {
		trusted, err := clientip.ParseTrusted([]string{"127.0.0.1"})
		require.NoError(t, err)
		addr := start(t, &ProxyProtocolOptions{Enabled: true, Trusted: trusted})
		_, body := proxyRoundTrip(t, addr, v1)
		require.Equal(t, "203.0.113.9:4242", body)
	})

	t.Run("disabled", func(t *testing.T) {
		addr := start(t, nil)
		status, _ := proxyRoundTrip(t, addr, v1)
		require.Equal(t, "400 Bad Request", status)
	})
}

func TestObservedConnectionProxyTLV(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln := (&ProxyProtocolOptions{Enabled: true}).wrap(tcp)
	defer ln.Close()
	accept := func(t *testing.T, send func(net.Conn)) *observedConnection {
		t.Helper()
		client, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })
		send(client)
		c, err := ln.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return &observedConnection{Conn: c}
	}
	const vpce = 0xEA
	withTLVs := accept(t, func(c net.Conn) {
		h := proxyproto.HeaderProxyFromAddrs(2,
			&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 4242},
			&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80})
		require.NoError(t, h.SetTLVs([]proxyproto.TLV{
			{Type: proxyproto.PP2_TYPE_AUTHORITY, Value: []byte("shop.example.com")},
			{Type: vpce, Value: []byte("first")},
			{Type: vpce, Value: []byte("second")},
		}))
		_, err := h.WriteTo(c)
		require.NoError(t, err)
	})
	v, ok := withTLVs.ProxyTLV(vpce)
	require.True(t, ok)
	require.Equal(t, "first", string(v))
	v, ok = withTLVs.ProxyTLV(byte(proxyproto.PP2_TYPE_AUTHORITY))
	require.True(t, ok)
	require.Equal(t, "shop.example.com", string(v))
	_, ok = withTLVs.ProxyTLV(0xEB)
	require.False(t, ok)

	// a version 1 header has no TLVs, and a connection with no header has nothing at all
	v1 := accept(t, func(c net.Conn) {
		_, err := io.WriteString(c, "PROXY TCP4 203.0.113.9 10.0.0.1 4242 80\r\n")
		require.NoError(t, err)
	})
	_, ok = v1.ProxyTLV(vpce)
	require.False(t, ok)
	bare := accept(t, func(c net.Conn) {
		_, err := io.WriteString(c, "hello")
		require.NoError(t, err)
	})
	_, ok = bare.ProxyTLV(vpce)
	require.False(t, ok)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_, ok = (&observedConnection{Conn: server}).ProxyTLV(vpce)
	require.False(t, ok)

	// TLVs that do not parse are no TLVs
	broken := accept(t, func(c net.Conn) {
		h := proxyproto.HeaderProxyFromAddrs(2,
			&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 4242},
			&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80})
		raw, err := h.Format()
		require.NoError(t, err)
		// one trailing byte cannot be a TLV, which needs three; the length covers it
		raw[15]++
		_, err = c.Write(append(raw, 0xEA))
		require.NoError(t, err)
	})
	_, ok = broken.ProxyTLV(vpce)
	require.False(t, ok)
}
