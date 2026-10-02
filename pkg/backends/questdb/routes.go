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

package questdb

import (
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/local"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/redirect"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.Backend.RegisterHandlers(handlers.Lookup{
		"health":                   http.HandlerFunc(c.HealthHandler),
		"proxy":                    http.HandlerFunc(c.ProxyHandler),
		handlers.NameLocalResponse: http.HandlerFunc(local.HandleLocalResponse),
		handlers.NameRedirect:      http.HandlerFunc(redirect.HandleRedirect),
	})
}

// DefaultPathConfigs passes every HTTP endpoint through unchanged. The
// catch-all deliberately leaves /execute, /exec, /validate, /exp, /imp,
// Web Console and QWP upgrade paths to the origin while HTTP caching remains
// disabled for this provider.
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.List {
	return po.List{{
		Path:          "/",
		HandlerName:   providers.Proxy,
		Methods:       methods.AllHTTPMethods(),
		MatchType:     matching.PathMatchTypePrefix,
		MatchTypeName: matching.PathMatchNamePrefix,
	}}
}

// ProxyHandler relays the inbound HTTP request to QuestDB without caching.
func (c *Client) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DoProxy(w, r, true)
}
