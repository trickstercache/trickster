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
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Graphite API paths
const (
	healthPath  = "/metrics/find"
	healthQuery = "query=*"
)

func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.TimeseriesBackend.RegisterHandlers(
		handlers.Lookup{
			// This is the registry of handlers that Trickster supports for Graphite,
			// and are able to be referenced by name (map key) in Config Files
			"health":        http.HandlerFunc(c.HealthHandler),
			"proxycache":    http.HandlerFunc(c.ObjectProxyCacheHandler),
			providers.Proxy: http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider.
// Proxy-only for now: the per-endpoint path list (/render to the render
// handler, /metrics/* and /tags/* to proxycache, ...) lands with the render
// handler in a later phase.
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.List {
	return po.List{
		{
			Path:          "/",
			HandlerName:   providers.Proxy,
			Methods:       methods.GetAndPost(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: matching.PathMatchNamePrefix,
		},
	}
}
