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

package graphite

import (
	"fmt"
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Graphite API paths
const (
	healthPath  = "/metrics/find"
	healthQuery = "query=*"
	renderPath  = "/render"
	expandPath  = "/metrics/expand"
)

// Handler names
const (
	handlerRender     = "render"
	handlerProxyCache = "proxycache"
)

func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.TimeseriesBackend.RegisterHandlers(
		handlers.Lookup{
			// This is the registry of handlers that Trickster supports for Graphite,
			// and are able to be referenced by name (map key) in Config Files
			"health":          http.HandlerFunc(c.HealthHandler),
			handlerRender:     http.HandlerFunc(c.RenderHandler),
			handlerProxyCache: http.HandlerFunc(c.ObjectProxyCacheHandler),
			providers.Proxy:   http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider:
// /render accelerated, metadata object-cached with TTLs, all else proxied
func (c *Client) DefaultPathConfigs(o *bo.Options) po.List {
	short := map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, 30)}
	long := map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, 3600)}
	paths := po.List{
		{
			Path:        renderPath,
			HandlerName: handlerRender,
			Methods:     methods.GetAndPost(),
			// from/until/now/format are absent by design: the extent is the
			// DPC's concern and the output format is applied at marshal time
			CacheKeyParams: []string{upTarget, "xFilesFactor", "local"},
			MatchType:      matching.PathMatchTypeExact,
			MatchTypeName:  matching.PathMatchNameExact,
		},
	}
	for _, p := range []string{healthPath, expandPath, "/metrics/index.json", "/tags"} {
		paths = append(paths, &po.Options{
			Path: p, HandlerName: handlerProxyCache, Methods: methods.GetAndPost(),
			CacheKeyParams: []string{"*"}, ResponseHeaders: short,
			MatchType: matching.PathMatchTypeExact, MatchTypeName: matching.PathMatchNameExact,
		})
	}
	paths = append(paths, &po.Options{
		Path: "/tags/", HandlerName: handlerProxyCache, Methods: methods.GetAndPost(),
		CacheKeyParams: []string{"*"}, ResponseHeaders: short,
		MatchType: matching.PathMatchTypePrefix, MatchTypeName: matching.PathMatchNamePrefix,
	})
	for _, p := range []string{"/functions", "/version"} {
		paths = append(paths, &po.Options{
			Path: p, HandlerName: handlerProxyCache, Methods: []string{http.MethodGet},
			CacheKeyParams: []string{"*"}, ResponseHeaders: long,
			MatchType: matching.PathMatchTypeExact, MatchTypeName: matching.PathMatchNameExact,
		})
	}
	paths = append(paths, &po.Options{
		Path: "/", HandlerName: providers.Proxy, Methods: methods.GetAndPost(),
		MatchType: matching.PathMatchTypePrefix, MatchTypeName: matching.PathMatchNamePrefix,
	})
	if o != nil {
		if auth, err := originAuthHeader(o.Graphite); err == nil {
			if err = injectOriginAuth(paths, auth); err != nil {
				logger.Error("failed to inject origin auth", logging.Pairs{
					"detail": err.Error(),
				})
			}
		}
	}
	return paths
}
