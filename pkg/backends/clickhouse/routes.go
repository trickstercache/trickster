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

package clickhouse

import (
	"net/http"
	"slices"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

var transportParams = []string{
	"query_id", "session_timeout", "session_check", "send_progress_in_http_headers",
	"http_headers_progress_interval_ms", "wait_end_of_query", "buffer_size", "log_comment",
	"log_queries", "quota_key", "add_http_cors_header",
}

func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.TimeseriesBackend.RegisterHandlers(
		handlers.Lookup{
			// This is the registry of handlers that Trickster supports for ClickHouse,
			// and are able to be referenced by name (map key) in Config Files
			"health":        http.HandlerFunc(c.HealthHandler),
			"query":         http.HandlerFunc(c.QueryHandler),
			providers.Proxy: http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.List {
	return po.List{
		{
			Path:          "/ping",
			HandlerName:   "health",
			Methods:       []string{http.MethodGet},
			MatchType:     matching.PathMatchTypeExact,
			MatchTypeName: matching.PathMatchNameExact,
		},
		{
			Path:          "/",
			HandlerName:   "query",
			Methods:       methods.GetAndPost(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: matching.PathMatchNamePrefix,
			// every other parameter is a query parameter or setting that can change the result
			CacheKeyParams:         []string{"*"},
			CacheKeyParamsExcluded: slices.Clone(transportParams),
		},
	}
}
