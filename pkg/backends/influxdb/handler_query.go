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
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// QueryHandler handles timeseries requests for InfluxDB and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {
	f := iofmt.Detect(r)
	switch {
	case f.IsV3SQL():
		if !isV3SelectQuery(r) {
			c.ProxyHandler(w, r)
			return
		}
	case f.IsV3InfluxQL():
		if !isV3SelectQuery(r) {
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

// isV3SelectQuery checks if a v3 request contains a SELECT query
func isV3SelectQuery(r *http.Request) bool {
	var q string
	if methods.HasBody(r.Method) {
		b, err := request.GetBody(r)
		if err != nil || len(b) == 0 {
			return false
		}
		q = string(b)
	} else {
		q = r.URL.Query().Get(isql.ParamQuery)
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
		return parseV3InfluxQL(r, f)
	case f.IsInfluxQL():
		return influxql.ParseTimeRangeQuery(r, f)
	case f.IsFlux():
		return flux.ParseTimeRangeQuery(r, f)
	}
	return nil, nil, false, errors.ErrBadRequest
}

// parseV3InfluxQL reuses the v1 InfluxQL parser but sets v3 output format
func parseV3InfluxQL(r *http.Request, f iofmt.Format,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	// v3 InfluxQL uses the same query language as v1, but the query arrives
	// in the "q" param (GET or POST body) and the response format is determined
	// by the "format" param. We reuse the v1 parser by temporarily swapping
	// the format to InfluxQL, then override the OutputFormat for v3 response.
	v1f := iofmt.InfluxqlGet
	if r.Method == http.MethodPost {
		v1f = iofmt.InfluxqlPost
	}
	trq, rlo, canOPC, err := influxql.ParseTimeRangeQuery(r, v1f)
	if err != nil {
		return trq, rlo, canOPC, err
	}
	if rlo != nil {
		rlo.OutputFormat = iofmt.V3OutputFormat(r)
	}
	if trq != nil && trq.ParsedQuery != nil {
		trq.ParsedQuery = &isql.V3InfluxQLQuery{Inner: trq.ParsedQuery}
	}
	return trq, rlo, canOPC, nil
}
