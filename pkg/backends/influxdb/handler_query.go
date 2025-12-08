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
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
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

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	f := iofmt.Detect(r)
	switch {
	case f.IsInfluxQL():
		return influxql.ParseTimeRangeQuery(r, f)
	case f.IsFlux():
		return flux.ParseTimeRangeQuery(r, f)
	}
	return nil, nil, false, errors.ErrBadRequest
}
