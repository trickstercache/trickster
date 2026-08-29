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

package influxdb

import (
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	isql "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/sql"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// QueryHandler handles timeseries requests for InfluxDB and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {
	f := iofmt.Detect(r)
	switch {
	case f.IsV3SQL(), f.IsV3InfluxQL():
		// formats Trickster cannot reserialize (parquet, pretty, ...) are
		// proxied through untouched, per docs/influxdb.md
		if !isV3SelectQuery(r) || !isql.SupportedV3Format(r) {
			c.ProxyHandler(w, r)
			return
		}
	case f.IsInfluxQL():
		qp, _, _ := params.GetRequestValues(r)
		// skip non-selects
		if q := qp.Get(influxql.ParamQuery); !strings.Contains(strings.ToLower(q), "select ") {
			c.ProxyHandler(w, r)
			return
		}
	case f.IsFlux():
		b, err := request.GetBody(r)
		if err != nil || len(b) == 0 ||
			!strings.Contains(strings.ToLower(string(b)), "from(") {
			c.ProxyHandler(w, r)
			return
		}
	}
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}

// isV3SelectQuery checks if a v3 request contains a SELECT query.
func isV3SelectQuery(r *http.Request) bool {
	q, err := isql.ExtractQuery(r)
	if err != nil || q == "" {
		return false
	}
	return slices.Contains(strings.Fields(strings.ToLower(q)), "select")
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	f := iofmt.Detect(r)
	switch {
	case f.IsV3SQL():
		return isql.ParseTimeRangeQuery(r, f)
	case f.IsV3InfluxQL():
		return isql.ParseV3InfluxQL(r, f)
	case f.IsInfluxQL():
		return influxql.ParseTimeRangeQuery(r, f)
	case f.IsFlux():
		return flux.ParseTimeRangeQuery(r, f)
	}
	return nil, nil, false, errors.ErrBadRequest
}
