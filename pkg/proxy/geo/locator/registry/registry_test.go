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

package registry

import (
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/feed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	_, err := New(nil)
	require.ErrorIs(t, err, ErrNilOptions)
	_, err = New(&options.Options{Name: "x", Provider: "nope"})
	require.ErrorContains(t, err, `no implementation registered for provider "nope"`)

	o := &options.Options{Name: "x", Provider: providers.Geofeed,
		Geofeed: &geofeedopts.Options{Entries: []string{"192.0.2.0/24,US"}}}
	l, err := New(o)
	require.NoError(t, err)
	loc, err := l.Locate(netip.MustParseAddr("192.0.2.1"))
	require.NoError(t, err)
	require.Equal(t, "US", loc.String())
	require.NoError(t, l.Close())

	o.Geofeed.Entries = []string{"bad"}
	_, err = New(o)
	require.ErrorIs(t, err, feed.ErrInvalidLine)
	require.ErrorContains(t, err, `geo locator "x"`)
}
