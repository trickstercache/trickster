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

package stream

import (
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/stretchr/testify/require"
)

var (
	clientFrance = netip.MustParseAddrPort("198.51.100.1:4000")
	clientUS     = netip.MustParseAddrPort("192.0.2.1:4000")
)

func compile(t *testing.T, action options.Action) *acl.ACL {
	t.Helper()
	feed, err := geofeed.New(t.Name(), &geofeedopts.Options{Entries: []string{
		"198.51.100.0/24,FR", "192.0.2.0/24,US",
	}})
	require.NoError(t, err)
	a, err := acl.Compile(&options.Options{Name: t.Name(), Deny: []string{"FR"}, Action: action}, feed, t.Name())
	require.NoError(t, err)
	return a
}

func TestForBackend(t *testing.T) {
	a := ForBackend(compile(t, 0))
	require.Equal(t, l4.Reject, a.Peer(l4.Flow{Client: clientFrance}))
	require.Equal(t, l4.Allow, a.Peer(l4.Flow{Client: clientUS}))
	require.Equal(t, l4.Allow, a.Flow(l4.Flow{Client: clientFrance}), "a tcp flow is judged at its peer only")
	require.Equal(t, l4.Allow, a.Datagram(l4.Flow{Client: clientFrance}, 1))
	require.False(t, a.Datagrams())

	// count relays and counts
	require.Equal(t, l4.Allow, ForBackend(compile(t, options.ActionCount)).Peer(l4.Flow{Client: clientFrance}))
}

type upstream struct{ l4.Upstream } // a route the table can hold, never dialed

func TestForRoutes(t *testing.T) {
	secure, other := &upstream{}, &upstream{}
	table := l4.NewTable()
	require.NoError(t, table.Add("secure.example.com", secure))
	require.NoError(t, table.Add("", other))
	a := ForRoutes(table, map[l4.Upstream]*acl.ACL{secure: compile(t, 0)})
	require.Equal(t, l4.Allow, a.Peer(l4.Flow{Client: clientFrance}), "a tls flow is judged once its name is known")
	require.Equal(t, l4.Reject, a.Flow(l4.Flow{Client: clientFrance, ServerName: "Secure.Example.com."}))
	require.Equal(t, l4.Allow, a.Flow(l4.Flow{Client: clientUS, ServerName: "secure.example.com"}))
	require.Equal(t, l4.Allow, a.Flow(l4.Flow{Client: clientFrance, ServerName: "other.example.com"}))
}
