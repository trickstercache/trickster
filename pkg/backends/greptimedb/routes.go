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

package greptimedb

import (
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

func (c *Client) RegisterHandlers(handlers.Lookup) {
	lookup := c.Client.HandlerLookup()
	lookup["sql"] = http.HandlerFunc(c.QueryHandler)
	lookup["health"] = http.HandlerFunc(c.HealthHandler)
	lookup["proxy"] = http.HandlerFunc(c.ProxyHandler)
	c.TimeseriesBackend.RegisterHandlers(lookup)
}

// DefaultPathConfigs preserves GreptimeDB's paths, including non-query APIs.
func (c *Client) DefaultPathConfigs(o *bo.Options) po.List {
	paths := po.List{{
		Path: "/", HandlerName: providers.Proxy, Methods: methods.AllHTTPMethods(),
		MatchType: matching.PathMatchTypePrefix, MatchTypeName: matching.PathMatchNamePrefix,
	}, {
		// The exact route must not mask catch-all passthrough for other methods.
		Path: "/v1/sql", HandlerName: "sql", Methods: methods.AllHTTPMethods(),
		MatchType: matching.PathMatchTypeExact, MatchTypeName: matching.PathMatchNameExact,
		CacheKeyParams:  []string{"sql", "db", "format", "epoch", "limit", "compression"},
		CacheKeyHeaders: []string{"X-Greptime-Db-Name", "X-Greptime-Timezone", "X-Greptime-Auth"},
	}}
	hooks := promHooks()
	promPaths := prometheus.Without(prometheus.SupportedPaths(o), "alerts", "proxycache", "admin", "proxy")
	promPaths = prometheus.WithPathPrefix(promPaths, hooks.PathPrefix)
	promPaths = prometheus.WithCacheKeyParams(promPaths, hooks.CacheKeyParams...)
	promPaths = prometheus.WithCacheKeyHeaders(promPaths, hooks.CacheKeyHeaders...)
	for _, p := range promPaths {
		// The request hook relays unsupported methods instead of masking the catch-all.
		p.Methods = methods.AllHTTPMethods()
		if o != nil && p.HandlerName == "query" {
			o.FastForwardPath = p.Clone()
		}
	}
	return append(paths, promPaths...)
}

// MergeablePaths excludes unsupported endpoints and the separate SQL surface.
func (c *Client) MergeablePaths() []string {
	paths := c.Client.MergeablePaths()
	supported := c.DefaultPathConfigs(nil)
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if p := supported.Match(http.MethodGet, path); p != nil && p.HandlerName != providers.Proxy {
			out = append(out, path)
		}
	}
	return out
}

// ProxyHandler relays HTTP requests without caching.
func (c *Client) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DoProxy(w, r, true)
}
