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

package druid

import (
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

const (
	handlerHealth     = "health"
	handlerQuery      = "query"
	handlerProxyCache = "proxycache"
)

// RegisterHandlers registers the handlers exposed by the Druid provider.
func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.TimeseriesBackend.RegisterHandlers(handlers.Lookup{
		handlerHealth:     http.HandlerFunc(c.HealthHandler),
		handlerQuery:      http.HandlerFunc(c.QueryHandler),
		handlerProxyCache: http.HandlerFunc(c.ObjectProxyCacheHandler),
		providers.Proxy:   http.HandlerFunc(c.ProxyHandler),
	})
}

// DefaultPathConfigs returns Druid's query, metadata, health, and proxy routes.
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.List {
	return po.List{
		{
			Path:          "/druid/v2",
			HandlerName:   handlerQuery,
			Methods:       []string{http.MethodPost},
			MatchType:     matching.PathMatchTypeExact,
			MatchTypeName: matching.PathMatchNameExact,
		},
		{
			Path:          "/druid/v2/sql/task",
			HandlerName:   providers.Proxy,
			Methods:       []string{http.MethodPost},
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: matching.PathMatchNamePrefix,
		},
		{
			Path:            "/druid/v2/sql",
			HandlerName:     handlerProxyCache,
			Methods:         []string{http.MethodPost},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   matching.PathMatchNamePrefix,
			CacheKeyBody:    true,
			CacheKeyParams:  []string{"*"},
			CacheKeyHeaders: []string{headers.NameContentType},
		},
		{
			Path:           "/druid/v2/datasources",
			HandlerName:    handlerProxyCache,
			Methods:        []string{http.MethodGet},
			CacheKeyParams: []string{"*"},
			MatchType:      matching.PathMatchTypePrefix,
			MatchTypeName:  matching.PathMatchNamePrefix,
		},
		{
			Path:          "/status/health",
			HandlerName:   handlerHealth,
			Methods:       []string{http.MethodGet},
			MatchType:     matching.PathMatchTypeExact,
			MatchTypeName: matching.PathMatchNameExact,
		},
		{
			Path:          "/",
			HandlerName:   providers.Proxy,
			Methods:       []string{"*"},
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: matching.PathMatchNamePrefix,
		},
	}
}
