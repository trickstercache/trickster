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

package victoriametrics

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

const (
	handlerGraphite      = "graphite"
	handlerGraphiteProxy = "graphite_proxy"
	handlerProxyCache    = "proxycache"
	// a shared max-age is appended, never set, so a no-store from partialTransport outweighs it
	appendCacheControl = "+" + headers.NameCacheControl
	discoveryMaxAge    = 30
	storageStepHeader  = "Storage-Step"
)

// RegisterHandlers registers the MetricsQL handlers, which prepareRequest guards, and the
// Graphite handlers.
func (c *Client) RegisterHandlers(handlers.Lookup) {
	lookup := c.prom.HandlerLookup()
	delete(lookup, "alerts")
	delete(lookup, "admin")
	lookup[handlerGraphite] = http.HandlerFunc(c.GraphiteHandler)
	lookup[handlerGraphiteProxy] = http.HandlerFunc(c.GraphiteProxyHandler)
	c.prom.TimeseriesBackend.RegisterHandlers(lookup)
}

// DefaultPathConfigs returns VictoriaMetrics' MetricsQL and Graphite routes. Writes, imports,
// exports, admin and internal APIs fall through to the uncached catch-all.
func (c *Client) DefaultPathConfigs(o *bo.Options) po.List {
	paths := prometheus.Without(prometheus.SupportedPaths(o), "alerts", "admin")
	paths = prometheus.WithCacheKeyParams(paths, resultParams...)
	for _, p := range paths {
		appendSharedMaxAge(p)
		switch p.HandlerName {
		case handlerProxyCache:
			// these routes' parameters aren't classified, so every one is part of the identity
			p.CacheKeyParams = []string{"*"}
		case providers.Proxy:
			p.Methods = methods.AllHTTPMethods()
		case "query":
			if o != nil {
				o.FastForwardPath = p.Clone()
			}
		}
	}
	return append(paths, graphitePaths(o)...)
}

// appendSharedMaxAge turns a path's Cache-Control into an appended value.
func appendSharedMaxAge(p *po.Options) {
	if v, ok := p.ResponseHeaders[headers.NameCacheControl]; ok {
		delete(p.ResponseHeaders, headers.NameCacheControl)
		p.ResponseHeaders[appendCacheControl] = v
	}
}

// graphitePaths returns the Graphite API routes, each also under VictoriaMetrics' /graphite alias.
func graphitePaths(o *bo.Options) po.List {
	renderAge := discoveryMaxAge
	if o != nil && o.TimeseriesTTL > 0 {
		renderAge = int(time.Duration(o.TimeseriesTTL) / time.Second)
	}
	cached := func(path string, mt matching.PathMatchType, maxAge int, ms ...string) *po.Options {
		var queryTypes []string
		if slices.Contains(ms, methods.MethodQuery) {
			// a QUERY reaches the origin as POST, which takes a form body
			queryTypes = []string{headers.ValueXFormURLEncoded}
		}
		return &po.Options{
			Path: path, HandlerName: handlerGraphite, Methods: ms, QueryMediaTypes: queryTypes,
			CacheKeyParams: []string{"*"}, CacheKeyHeaders: []string{storageStepHeader},
			ResponseHeaders: map[string]string{
				appendCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, maxAge),
			},
			MatchType: mt, MatchTypeName: matching.Values[mt],
		}
	}
	relayed := func(path string) *po.Options {
		return &po.Options{
			Path: path, HandlerName: handlerGraphiteProxy, Methods: methods.AllHTTPMethods(),
			MatchType: matching.PathMatchTypeExact, MatchTypeName: matching.PathMatchNameExact,
		}
	}
	get := []string{http.MethodGet}
	var out po.List
	for _, prefix := range []string{"", graphiteSegment} {
		out = append(out,
			cached(prefix+"/render", matching.PathMatchTypeExact, renderAge, methods.QueryableMethods()...),
			cached(prefix+"/metrics/find", matching.PathMatchTypeExact, discoveryMaxAge, methods.QueryableMethods()...),
			cached(prefix+"/metrics/expand", matching.PathMatchTypeExact, discoveryMaxAge, methods.QueryableMethods()...),
			cached(prefix+"/metrics/index.json", matching.PathMatchTypeExact, discoveryMaxAge, get...),
			cached(prefix+"/tags", matching.PathMatchTypeExact, discoveryMaxAge, get...),
			cached(prefix+"/tags/", matching.PathMatchTypePrefix, discoveryMaxAge, get...),
			cached(prefix+"/functions", matching.PathMatchTypeExact, discoveryMaxAge, get...),
			cached(prefix+"/functions/", matching.PathMatchTypePrefix, discoveryMaxAge, get...),
			relayed(prefix+"/tags/tagSeries"),
			relayed(prefix+"/tags/tagMultiSeries"),
			relayed(prefix+"/tags/delSeries"),
		)
	}
	return out
}

// graphiteSuffix strips VictoriaMetrics' /graphite alias from a Graphite API path.
func graphiteSuffix(path string) string {
	if rest, ok := strings.CutPrefix(path, graphiteSegment); ok && strings.HasPrefix(rest, "/") {
		return rest
	}
	return path
}
