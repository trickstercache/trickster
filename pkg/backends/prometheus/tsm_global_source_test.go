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
	"net/http"
	"net/url"
	"testing"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

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
		require.Equal(t, po.SeriesVolatileWindow, source.(global.Source).VolatileWindow())
		again, _ := c.TSMGlobalSource()
		require.Same(t, source, again)
	})
	t.Run("another prometheus reads its series api", func(t *testing.T) {
		source, err := newGlobalSourceClient(t, origin, &po.Options{SeriesAPI: po.SeriesAPIRemoteRead}).TSMGlobalSource()
		require.NoError(t, err)
		require.Equal(t, po.SeriesAPIRemoteRead, source.SeriesAPI())
		require.Zero(t, source.(global.Source).VolatileWindow())
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
		require.Equal(t, po.SeriesVolatileWindow, source.(global.Source).VolatileWindow())
		require.NoError(t, source.(*global.StoreAPI).Close())
		require.NotEmpty(t, source.Identity())
	})
}

func TestGlobalSourceIdentity(t *testing.T) {
	const (
		origin    = "http://mimir:8080/prometheus"
		other     = "http://mimir-2:8080/prometheus"
		team      = "X-Team"
		tenant    = "X-Scope-OrgID"
		tenantA   = "tenant-a"
		tenantB   = "tenant-b"
		certA     = "/certs/a.pem"
		accessKey = "AKIA"
		region    = "us-east-1"
	)
	route := func(path string, requestHeaders map[string]string, keyHeaders ...string) *pathopts.Options {
		pc := pathopts.New()
		pc.Path, pc.MatchType, pc.MatchTypeName = path, matching.PathMatchTypeExact, matching.PathMatchNameExact
		pc.Methods = []string{http.MethodGet, http.MethodPost}
		pc.RequestHeaders, pc.CacheKeyHeaders = requestHeaders, keyHeaders
		return pc
	}
	identity := func(endpoint string, change func(*bo.Options)) string {
		o := bo.New()
		o.Paths = pathopts.List{
			route(APIPath+mnQueryRange, map[string]string{tenant: tenantA}),
			route(APIPath+mnLabels, map[string]string{tenant: tenantA}),
		}
		if change != nil {
			change(o)
		}
		u, err := url.Parse(endpoint)
		require.NoError(t, err)
		return globalSourceIdentity(o, po.SeriesAPIRemoteRead, u)
	}
	base := identity(origin, nil)
	require.Equal(t, base, identity(origin, nil), "an identity is stable")
	require.NotEqual(t, base, identity(other, nil), "the endpoint")
	require.NotEqual(t, base, identity(origin, func(o *bo.Options) {
		o.Paths[0].RequestHeaders[tenant] = tenantB
	}), "a query path's request headers")
	require.Equal(t, base, identity(origin, func(o *bo.Options) {
		o.Paths[1].RequestHeaders[tenant] = tenantB
	}), "a path global evaluation never reads is not part of it")
	require.NotEqual(t, base, identity(origin, func(o *bo.Options) {
		o.TLS = &to.Options{ClientCertPath: certA}
	}), "the TLS client certificate")
	require.NotEqual(t, base, identity(origin, func(o *bo.Options) {
		o.SigV4 = &taws.Options{Region: region, AccessKey: accessKey}
	}), "the SigV4 principal")

	paths := pathopts.List{
		route(APIPath+mnQuery, nil, team, tenant), route(APIPath+mnQueryRange, nil, "x-team"),
		route(APIPath+mnLabels, nil, "X-Other"),
	}
	require.Equal(t, []string{http.CanonicalHeaderKey(tenant), team}, globalKeyHeaders(paths),
		"query paths' key headers, once each, as header names, sorted")
	require.Empty(t, globalKeyHeaders(nil))
}
