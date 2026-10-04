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

package prometheus

import (
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
)

func newGlobalSourceClient(t *testing.T, origin string, p *po.Options) *Client {
	t.Helper()
	const name = "member"
	o := bo.New()
	o.Name = name
	o.Provider = providers.Prometheus
	o.OriginURL = origin
	o.Prometheus = p
	c, err := NewClient(name, o, nil, nil, nil, nil)
	require.NoError(t, err)
	return c.(*Client)
}

func TestTSMGlobalSource(t *testing.T) {
	const origin = "http://mimir:8080/prometheus"
	t.Run("mimir reads remote read", func(t *testing.T) {
		c := newGlobalSourceClient(t, origin, &po.Options{Flavor: po.FlavorMimir})
		source, err := c.TSMGlobalSource()
		require.NoError(t, err)
		require.IsType(t, &global.RemoteRead{}, source)
		require.Equal(t, po.SeriesAPIRemoteRead, source.SeriesAPI())
		again, _ := c.TSMGlobalSource()
		require.Same(t, source, again)
	})
	t.Run("no series api", func(t *testing.T) {
		_, err := newGlobalSourceClient(t, origin, &po.Options{}).TSMGlobalSource()
		require.ErrorIs(t, err, errNoSeriesAPI)
	})
	t.Run("bad origin", func(t *testing.T) {
		_, err := newGlobalSourceClient(t, "mimir:8080", &po.Options{Flavor: po.FlavorMimir}).TSMGlobalSource()
		require.ErrorIs(t, err, po.ErrSeriesEndpoint)
	})
	t.Run("storeapi", func(t *testing.T) {
		source, err := newGlobalSourceClient(t, origin, &po.Options{
			Flavor: po.FlavorThanos, SeriesEndpoint: po.StoreAPISchemeTLS + "://thanos:10901",
		}).TSMGlobalSource()
		require.NoError(t, err)
		require.IsType(t, &global.StoreAPI{}, source)
		require.Equal(t, po.SeriesAPIStoreAPI, source.SeriesAPI())
		require.NoError(t, source.(*global.StoreAPI).Close())
	})
}
