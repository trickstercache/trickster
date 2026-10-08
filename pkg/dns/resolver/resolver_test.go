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

package resolver

import (
	"context"
	"net"
	"testing"
	"time"

	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dnsserver"

	"github.com/stretchr/testify/require"
)

const (
	testSRVName  = "_prom._tcp.example.com."
	testHostName = "prom.example.com."
	testTargetA  = "prom-a.example.com."
	testAddr     = "10.0.0.1"
)

func TestMinTTL(t *testing.T) {
	require.Equal(t, 30*time.Second, MinTTL(0, 30))
	require.Equal(t, 10*time.Second, MinTTL(30*time.Second, 10))
	require.Equal(t, 10*time.Second, MinTTL(10*time.Second, 30))
}

func TestNewSelection(t *testing.T) {
	require.IsType(t, &directResolver{}, New("10.0.0.53:53"))
	// with no server configured, either the resolv.conf-backed direct
	// resolver or the stdlib fallback is acceptable
	require.NotNil(t, New(""))
	require.IsType(t, &stdResolver{}, NewStd(nil))
}

func TestDirectSRVWithAdditional(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeSRV,
		dnsserver.SRV(testSRVName, 30, 10, 2, 9090, testTargetA),
		dnsserver.SRV(testSRVName, 20, 10, 1, 9091, "prom-b.example.com."),
	)
	srv.SetAdditional(
		dnsserver.A("PROM-A.example.com.", 15, testAddr),
		dnsserver.AAAA(testTargetA, 40, "fd00::1"),
	)
	answer, err := NewDirect(srv.Addr()).LookupSRV(t.Context(), testSRVName)
	require.NoError(t, err)
	require.Len(t, answer.Records, 2)
	require.Equal(t, 20*time.Second, answer.TTL)
	require.Len(t, answer.Additional, 1, "additional names are folded to lowercase")
	ia := answer.Additional[testTargetA]
	require.Equal(t, []string{testAddr, "fd00::1"}, ia.Addrs)
	require.Equal(t, 15*time.Second, ia.TTL)
}

func TestDirectErrors(t *testing.T) {
	srv := dnsserver.New(t)
	r := NewDirect(srv.Addr())
	srv.SetRCode(dnsclient.RCodeNameError)
	_, err := r.LookupSRV(t.Context(), testSRVName)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.LookupIP(t.Context(), testHostName)
	require.ErrorIs(t, err, ErrNotFound)

	srv.SetRCode(dnsclient.RCodeServerFailure)
	_, err = r.LookupSRV(t.Context(), testSRVName)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
}

func TestDirectLookupIP(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeA, dnsserver.A(testHostName, 30, testAddr))
	srv.Set(dnsclient.TypeAAAA, dnsserver.AAAA(testHostName, 10, "fd00::1"))
	ia, err := NewDirect(srv.Addr()).LookupIP(t.Context(), testHostName)
	require.NoError(t, err)
	require.Equal(t, []string{testAddr, "fd00::1"}, ia.Addrs)
	require.Equal(t, 10*time.Second, ia.TTL)
}

// TestStdResolver exercises the stdlib resolver against the in-process DNS
// server via a custom Dial
func TestStdResolver(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeSRV, dnsserver.SRV(testSRVName, 30,
		10, 2, 9090, testTargetA))
	srv.Set(dnsclient.TypeA, dnsserver.A(testHostName, 30, testAddr))
	srv.Set(dnsclient.TypeAAAA)

	r := NewStd(&net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Addr())
		},
	})
	answer, err := r.LookupSRV(t.Context(), testSRVName)
	require.NoError(t, err)
	require.Len(t, answer.Records, 1)
	require.Equal(t, testTargetA, answer.Records[0].Target)
	require.Equal(t, uint16(2), answer.Records[0].Weight)
	require.Zero(t, answer.TTL, "the stdlib resolver conveys no TTLs")

	ia, err := r.LookupIP(t.Context(), testHostName)
	require.NoError(t, err)
	require.Equal(t, []string{testAddr}, ia.Addrs)
	require.Zero(t, ia.TTL)

	// the stdlib surfaces an empty NOERROR answer as not found
	srv.Set(dnsclient.TypeA)
	_, err = r.LookupIP(t.Context(), testHostName)
	require.ErrorIs(t, err, ErrNotFound)
}
