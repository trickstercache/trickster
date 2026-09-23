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

package greptimedb

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/prometheus/prometheus/promql/parser"
)

const (
	promPath       = "/v1/prometheus"
	databaseParam  = "db"
	databaseHeader = "X-Greptime-Db-Name"
)

func promHooks() prometheus.Hooks {
	return prometheus.Hooks{
		PathPrefix:        promPath,
		CacheKeyParams:    []string{databaseParam, "lookback"},
		CacheKeyHeaders:   []string{databaseHeader, "X-Greptime-Timezone", "X-Greptime-Auth"},
		PrepareRequest:    preparePromRequest,
		PreserveQueryGrid: true,
	}
}

func isPromRange(r *http.Request) bool {
	return r != nil && r.URL != nil && strings.HasSuffix(r.URL.Path, promPath+prometheus.APIPath+"query_range")
}

func preparePromRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return false
	}
	if r.Method == http.MethodPost && strings.Contains(r.URL.Path, prometheus.APIPath+"label/") {
		return false
	}
	if rsc := request.GetResources(r); rsc != nil && rsc.PathConfig != nil && len(rsc.PathConfig.RequestParams) != 0 {
		return false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !validPromParams(values) {
		return false
	}
	if r.Method == http.MethodPost {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/x-www-form-urlencoded" {
			return false
		}
		body, err := request.GetBody(r)
		if err != nil {
			return false
		}
		form, err := url.ParseQuery(string(body))
		if err != nil || !validPromParams(form) {
			return false
		}
		// Greptime reads db only from the URL/context, and other fields prefer
		// the URL even when its explicit value is empty.
		for name, field := range form {
			if _, exists := values[name]; !exists && name != databaseParam {
				values[name] = field
			}
		}
	}
	if query := values.Get("query"); query != "" && !cacheablePromExpression(query) {
		return false
	}
	if r.Method == http.MethodPost {
		params.SetRequestValues(r, values)
	}
	return true
}

func cacheablePromExpression(query string) bool {
	expr, err := metricParser.ParseExpr(query)
	if err != nil {
		return false
	}
	valid := true
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if agg, ok := node.(*parser.AggregateExpr); ok && agg.Op == parser.COUNT_VALUES {
			// Greptime currently emits numeric count_values labels as value
			// fields, losing series identity and producing duplicate timestamps.
			valid = false
		}
		return nil
	})
	return valid
}

func validPromParams(values url.Values) bool {
	for name, fields := range values {
		switch name {
		case "match[]":
			continue
		case "query", "start", "end", "step", "time", databaseParam, "lookback", "stats":
			if len(fields) == 1 {
				continue
			}
		}
		return false
	}
	return true
}
