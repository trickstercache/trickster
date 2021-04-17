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

	"github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/proxy/methods"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/paths/matching"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
)

func (c *Client) registerHandlers() {
	c.handlersRegistered = true
	c.handlers = make(map[string]http.Handler)
	// This is the registry of handlers that Trickster supports for the Reverse Proxy Cache,
	// and are able to be referenced by name (map key) in Config Files
	c.handlers["health"] = http.HandlerFunc(c.HealthHandler)
	c.handlers["proxy"] = http.HandlerFunc(c.ProxyHandler)
	c.handlers["proxycache"] = http.HandlerFunc(c.ProxyCacheHandler)
	c.handlers["localresponse"] = http.HandlerFunc(handlers.HandleLocalResponse)
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	if !c.handlersRegistered {
		c.registerHandlers()
	}
	return c.handlers
}

// DefaultPathConfigs returns the default PathConfigs for the given OriginType
func (c *Client) DefaultPathConfigs(oc *oo.Options) map[string]*po.Options {

	cm := methods.CacheableHTTPMethods()
	um := methods.UncacheableHTTPMethods()

	paths := map[string]*po.Options{
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
