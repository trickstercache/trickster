/*
 * Copyright 2018 The Trickster Authors
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

package client_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/dns/client"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dnsserver"

	"github.com/stretchr/testify/require"
)

const testHost = "prom.example.com."

func testServer(t *testing.T) *dnsserver.Server {
	t.Helper()
	srv := dnsserver.New(t)
	srv.Set(client.TypeA,
		dnsserver.A(testHost, 300, "10.0.0.1"),
		dnsserver.A(testHost, 60, "10.0.0.2"),
	)
	srv.Set(client.TypeAAAA, dnsserver.AAAA(testHost, 120, "2001:db8::1"))
	srv.Set(client.TypeSRV,
		dnsserver.SRV("_prom._tcp.example.com.", 90, 10, 5, 9090, "prom-a.example.com."))
	return srv
}

func TestQueryA(t *testing.T) {
	srv := testServer(t)
	c := &client.Client{}
	// the unqualified name is what a config carries; Query qualifies it
	r, err := c.Query(t.Context(), srv.Addr(), "prom.example.com", client.TypeA)
	require.NoError(t, err)
	require.Equal(t, client.RCodeSuccess, r.RCode)
	require.True(t, r.Response)
	require.Len(t, r.Answers, 2)
	first := r.Answers[0].(*client.A)
	require.Equal(t, "10.0.0.1", first.Addr.String())
	require.Equal(t, uint32(300), first.Hdr.TTL)
	require.Equal(t, testHost, first.Hdr.Name)
	require.Equal(t, uint32(60), r.Answers[1].Header().TTL)
}

func TestQueryAAAAAndSRV(t *testing.T) {
	srv := testServer(t)
	c := &client.Client{}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeAAAA)
	require.NoError(t, err)
	require.Len(t, r.Answers, 1)
	require.Equal(t, "2001:db8::1", r.Answers[0].(*client.AAAA).Addr.String())

	r, err = c.Query(t.Context(), srv.Addr(), "_prom._tcp.example.com", client.TypeSRV)
	require.NoError(t, err)
	require.Len(t, r.Answers, 1)
	got := r.Answers[0].(*client.SRV)
	require.Equal(t, uint16(10), got.Priority)
	require.Equal(t, uint16(5), got.Weight)
	require.Equal(t, uint16(9090), got.Port)
	require.Equal(t, "prom-a.example.com.", got.Target)
	require.Equal(t, uint32(90), got.Hdr.TTL)
}

// TestTruncatedRetriesOverTCP proves a truncated UDP answer is re-asked on
// TCP, where the full record set comes back
func TestTruncatedRetriesOverTCP(t *testing.T) {
	srv := testServer(t)
	srv.SetTruncate(true)
	c := &client.Client{}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeA)
	require.NoError(t, err)
	require.False(t, r.Truncated)
	require.Len(t, r.Answers, 2)
}

func TestQueryOverTCP(t *testing.T) {
	srv := testServer(t)
	c := &client.Client{Net: "tcp"}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeA)
	require.NoError(t, err)
	require.Len(t, r.Answers, 2)
}

func TestQueryWithoutEDNS0(t *testing.T) {
	srv := testServer(t)
	c := &client.Client{UDPSize: -1}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeA)
	require.NoError(t, err)
	require.Len(t, r.Answers, 2)
}

// TestQueryFailureRCode: a server-side failure is a valid response, not a
// transport error; the caller decides what to do with the code
func TestQueryFailureRCode(t *testing.T) {
	srv := testServer(t)
	srv.SetRCode(client.RCodeServerFailure)
	c := &client.Client{}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeA)
	require.NoError(t, err)
	require.Equal(t, client.RCodeServerFailure, r.RCode)
	require.Equal(t, "SERVFAIL", r.RCode.String())
	require.Empty(t, r.Answers)
}

func TestQueryUnknownType(t *testing.T) {
	srv := dnsserver.New(t)
	c := &client.Client{}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeA)
	require.NoError(t, err)
	require.Empty(t, r.Answers, "an authoritative empty answer is not an error")
}

func TestQueryDialFailure(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	require.NoError(t, pc.Close())

	c := &client.Client{Timeout: 500 * time.Millisecond}
	_, err = c.Query(t.Context(), addr, testHost, client.TypeA)
	require.Error(t, err)

	_, err = c.Query(t.Context(), "not-a-host-port", testHost, client.TypeA)
	require.Error(t, err)

	c = &client.Client{Net: "tcp", Timeout: 500 * time.Millisecond}
	_, err = c.Query(t.Context(), addr, testHost, client.TypeA)
	require.Error(t, err)
}

func TestQueryUnencodableName(t *testing.T) {
	srv := testServer(t)
	c := &client.Client{}
	_, err := c.Query(t.Context(), srv.Addr(), "a..b", client.TypeA)
	require.ErrorIs(t, err, client.ErrInvalidName)
}

func TestQueryCanceledContext(t *testing.T) {
	srv := testServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := &client.Client{}
	_, err := c.Query(ctx, srv.Addr(), testHost, client.TypeA)
	require.Error(t, err)
}

func TestClientDialer(t *testing.T) {
	srv := testServer(t)
	c := &client.Client{Dialer: &net.Dialer{LocalAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}}}
	r, err := c.Query(t.Context(), srv.Addr(), testHost, client.TypeA)
	require.NoError(t, err)
	require.Len(t, r.Answers, 2)
}

// TestExchangeIgnoresUnrelatedDatagrams proves an off-path answer with a
// mismatched ID does not satisfy the query
func TestExchangeIgnoresUnrelatedDatagrams(t *testing.T) {
	spoof, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { spoof.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := spoof.ReadFrom(buf)
			if err != nil {
				return
			}
			bad := &client.Msg{ID: 0, Response: true}
			// the query IDs are random, so a zero ID is near-certainly wrong
			if wire, err := bad.Pack(); err == nil {
				spoof.WriteTo(wire, addr)
			}
			_ = n
		}
	}()

	c := &client.Client{Timeout: 300 * time.Millisecond}
	_, err = c.Query(t.Context(), spoof.LocalAddr().String(), testHost, client.TypeA)
	require.Error(t, err, "the deadline expires rather than accepting the reply")
}
