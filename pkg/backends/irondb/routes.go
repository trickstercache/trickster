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

package irondb

import (
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func (c *Client) RegisterHandlers(map[string]http.Handler) {

	c.TimeseriesBackend.RegisterHandlers(
		map[string]http.Handler{
			"health":    http.HandlerFunc(c.HealthHandler),
			mnRaw:       http.HandlerFunc(c.RawHandler),
			mnRollup:    http.HandlerFunc(c.RollupHandler),
			mnFetch:     http.HandlerFunc(c.FetchHandler),
			mnRead:      http.HandlerFunc(c.TextHandler),
			mnHistogram: http.HandlerFunc(c.HistogramHandler),
			mnFind:      http.HandlerFunc(c.FindHandler),
			mnState:     http.HandlerFunc(c.StateHandler),
			mnCAQL:      http.HandlerFunc(c.CAQLHandler),
			"proxy":     http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {

	paths := map[string]*po.Options{

		"/" + mnRaw + "/": {
			Path:            "/" + mnRaw + "/",
			HandlerName:     "RawHandler",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnRollup + "/": {
			Path:            "/" + mnRollup + "/",
			HandlerName:     "RollupHandler",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{upSpan, upEngine, upType},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnFetch: {
			Path:            "/" + mnFetch,
			HandlerName:     "FetchHandler",
			KeyHasher:       c.fetchHandlerDeriveCacheKey,
			Methods:         []string{http.MethodPost},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnRead + "/": {
			Path:            "/" + mnRead + "/",
			HandlerName:     "TextHandler",
			KeyHasher:       c.textHandlerDeriveCacheKey,
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{"*"},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnHistogram + "/": {
			Path:            "/" + mnHistogram + "/",
			HandlerName:     "HistogramHandler",
			Methods:         []string{http.MethodGet},
			KeyHasher:       c.histogramHandlerDeriveCacheKey,
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnFind + "/": {
			Path:            "/" + mnFind + "/",
			HandlerName:     "FindHandler",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{upQuery},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnState + "/": {
			Path:            "/" + mnState + "/",
			HandlerName:     "StateHandler",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{"*"},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnCAQL: {
			Path:            "/" + mnCAQL,
			HandlerName:     "CAQLHandler",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{upQuery, upCAQLQuery, upCAQLPeriod},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/" + mnCAQLPub + "/": {
			Path:            "/" + mnCAQLPub + "/",
			HandlerName:     "CAQLPubHandler",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{upQuery, upCAQLQuery, upCAQLPeriod},
			CacheKeyHeaders: []string{},
			MatchType:       matching.PathMatchTypePrefix,
			MatchTypeName:   "prefix",
		},

		"/": {
			Path:          "/",
			HandlerName:   "ProxyHandler",
			Methods:       []string{http.MethodGet},
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}

	return paths

}
