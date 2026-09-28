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
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/greptimedb/sql"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// QueryHandler serves GreptimeDB's HTTP SQL endpoint.
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(sqlResponseWriter{w}, r, c.sqlModeler)
}

// Rebuilt SQL results have no reusable origin execution metrics. Apply these
// headers at write time because DPC can serialize to a singleflight buffer.
type sqlResponseWriter struct{ http.ResponseWriter }

func (w sqlResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w sqlResponseWriter) prepare() {
	engine, _ := headers.ParseResultEngineStatus(w.Header().Get(headers.NameTricksterResult))
	if engine == "DeltaProxyCache" {
		w.Header().Set("X-Greptime-Execution-Time", "0")
		w.Header().Set("X-Greptime-Format", "greptimedb_v1")
		w.Header().Del("X-Greptime-Metrics")
	}
}

func (w sqlResponseWriter) WriteHeader(code int) {
	w.prepare()
	w.ResponseWriter.WriteHeader(code)
}

func (w sqlResponseWriter) Write(body []byte) (int, error) {
	w.prepare()
	return w.ResponseWriter.Write(body)
}

func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	if isPromRange(r) {
		return c.Client.ParseTimeRangeQuery(r)
	}
	a := analyzer.zoned
	if r != nil {
		timezone := r.Header.Get("X-Greptime-Timezone")
		if res := request.GetResources(r); res != nil && res.PathConfig != nil {
			// Request parameter overrides run after extent rendering. They must
			// not replace the statement or its bounds after cache analysis.
			if len(res.PathConfig.RequestParams) > 0 {
				return nil, nil, false, timeseries.ErrUnknownFormat
			}
			effective := r.Clone(r.Context())
			headers.UpdateRequestHeaders(effective, res.PathConfig.RequestHeaders)
			timezone = effective.Header.Get("X-Greptime-Timezone")
		}
		// HTTP's absent timezone is UTC, unlike pgwire's configurable default.
		switch strings.ToUpper(timezone) {
		case "", "UTC", "ETC/UTC", "+00:00", "-00:00":
			a = analyzer.utc
		}
	}
	return sql.ParseTimeRangeQuery(r, a)
}

func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) error {
	if isPromRange(r) {
		return c.Client.SetExtent(r, trq, extent)
	}
	return sql.SetExtent(r, trq, extent)
}
