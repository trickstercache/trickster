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

package reverseproxycache

import (
	"net/http"
	"strings"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func (c *Client) RegisterHandlers(map[string]http.Handler) {
	c.Backend.RegisterHandlers(
		map[string]http.Handler{
			"health":        http.HandlerFunc(c.HealthHandler),
			"proxy":         http.HandlerFunc(c.ProxyHandler),
			"proxycache":    http.HandlerFunc(c.ProxyCacheHandler),
			"localresponse": http.HandlerFunc(handlers.HandleLocalResponse),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {

	cm := methods.CacheableHTTPMethods()
	um := methods.UncacheableHTTPMethods()

	paths := po.Lookup{
		"/-" + strings.Join(cm, "-"): {
			Path:          "/",
			HandlerName:   "proxycache",
			Methods:       cm,
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
		"/-" + strings.Join(um, "-"): {
			Path:          "/",
			HandlerName:   "proxy",
			Methods:       um,
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}
	return paths
}
