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

package dnsserver

import (
	"net"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/dns/client"

	"github.com/stretchr/testify/require"
)

func TestServerServesBothTransports(t *testing.T) {
	srv := New(t)
	srv.Set(client.TypeA, A("host.example.com.", 30, "10.0.0.1"))
	_, _, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)

	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			c := &client.Client{Net: network}
			r, err := c.Query(t.Context(), srv.Addr(), "host.example.com", client.TypeA)
			require.NoError(t, err)
			require.Len(t, r.Answers, 1)
			require.Equal(t, uint32(30), r.Answers[0].Header().TTL)
		})
	}
}

func TestServerRCodeAndTruncate(t *testing.T) {
	srv := New(t)
	srv.Set(client.TypeAAAA, AAAA("host.example.com.", 30, "2001:db8::1"))
	srv.Set(client.TypeSRV, SRV("_svc._tcp.example.com.", 30, 1, 2, 80, "a.example.com"))
	c := &client.Client{}

	srv.SetRCode(client.RCodeNameError)
	r, err := c.Query(t.Context(), srv.Addr(), "host.example.com", client.TypeAAAA)
	require.NoError(t, err)
	require.Equal(t, client.RCodeNameError, r.RCode)

	// truncation applies to UDP only, so the client's TCP retry succeeds
	srv.SetRCode(client.RCodeSuccess)
	srv.SetTruncate(true)
	r, err = c.Query(t.Context(), srv.Addr(), "_svc._tcp.example.com", client.TypeSRV)
	require.NoError(t, err)
	require.False(t, r.Truncated)
	require.Len(t, r.Answers, 1)
	require.Equal(t, "a.example.com.", r.Answers[0].(*client.SRV).Target)
}

func TestServerStopIsIdempotent(t *testing.T) {
	srv := New(t)
	srv.Stop()
	srv.Stop()
}
