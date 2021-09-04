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

package prometheus

import (
	"fmt"
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func (c *Client) RegisterHandlers(map[string]http.Handler) {
	c.TimeseriesBackend.RegisterHandlers(
		map[string]http.Handler{
			"health":      http.HandlerFunc(c.HealthHandler),
			"query_range": http.HandlerFunc(c.QueryRangeHandler),
			"query":       http.HandlerFunc(c.QueryHandler),
			"series":      http.HandlerFunc(c.SeriesHandler),
			"proxycache":  http.HandlerFunc(c.ObjectProxyCacheHandler),
			"proxy":       http.HandlerFunc(c.ProxyHandler),
			"labels":      http.HandlerFunc(c.LabelsHandler),
			"alerts":      http.HandlerFunc(c.AlertsHandler),
			"admin":       http.HandlerFunc(c.UnsupportedHandler),
		},
	)
}

// MergeablePaths returns the list of Prometheus Paths for which Trickster supports
// merging multiple documents into a single response
func MergeablePaths() []string {
	return []string{
		"/api/v1/query_range",
		"/api/v1/query",
		"/api/v1/alerts",
		"/api/v1/series",
		"/api/v1/labels",
		"/api/v1/label/",
	}
}

// MergeablePaths returns the list of Prometheus Paths for which Trickster supports
// merging multiple documents into a single response
func (c *Client) MergeablePaths() []string {
	return MergeablePaths()
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {

	var rhts map[string]string
	if o != nil {
		rhts = map[string]string{
			headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, o.TimeseriesTTLMS/1000)}
	}
	rhinst := map[string]string{
		headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, 30)}

	paths := po.Lookup{

		APIPath + mnQueryRange: {
			Path:            APIPath + mnQueryRange,
			HandlerName:     mnQueryRange,
			Methods:         methods.GetAndPost(),
			CacheKeyParams:  []string{upQuery, upStep},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhts,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnQuery: {
			Path:            APIPath + mnQuery,
			HandlerName:     mnQuery,
			Methods:         methods.GetAndPost(),
			CacheKeyParams:  []string{upQuery, upTime},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnSeries: {
			Path:            APIPath + mnSeries,
			HandlerName:     mnSeries,
			Methods:         methods.GetAndPost(),
			CacheKeyParams:  []string{upMatch, upStart, upEnd},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnLabels: {
			Path:            APIPath + mnLabels,
			HandlerName:     "labels",
			Methods:         methods.GetAndPost(),
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnLabel + "/": {
			Path:            APIPath + mnLabel + "/",
			HandlerName:     "labels",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "prefix",
			MatchType:       matching.PathMatchTypePrefix,
			ResponseHeaders: rhinst,
		},

		APIPath + mnTargets: {
			Path:            APIPath + mnTargets,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnTargetsMeta: {
			Path:            APIPath + mnTargetsMeta,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{"match_target", "metric", "limit"},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnRules: {
			Path:            APIPath + mnRules,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnAlerts: {
			Path:            APIPath + mnAlerts,
			HandlerName:     "alerts",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnAlertManagers: {
			Path:            APIPath + mnAlertManagers,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnStatus: {
			Path:            APIPath + mnStatus,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "prefix",
			MatchType:       matching.PathMatchTypePrefix,
			ResponseHeaders: rhinst,
		},

		APIPath + "admin": {
			Path:          APIPath + "admin",
			HandlerName:   "admin",
			Methods:       methods.AllHTTPMethods(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},

		APIPath: {
			Path:          APIPath,
			HandlerName:   "proxy",
			Methods:       methods.GetAndPost(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},

		"/": {
			Path:          "/",
			HandlerName:   "proxy",
			Methods:       methods.GetAndPost(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}

	o.FastForwardPath = paths[APIPath+mnQuery].Clone()

	return paths

}
