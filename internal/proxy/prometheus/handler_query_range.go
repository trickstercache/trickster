/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package prometheus

import (
	"net/http"

	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/timeseries"
)

// QueryRangeHandler handles timeseries requests for Prometheus and processes them through the delta proxy cache
func (c *Client) QueryRangeHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	proxy.DeltaProxyCacheRequest(
		proxy.NewRequest(c.Name, otPrometheus, "QueryRangeHandler", r.Method, u, r.Header, c.Config.Timeout, r),
		w, c, c.Cache, c.Cache.Configuration().TimeseriesTTLSecs, false)
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *proxy.Request) (*timeseries.TimeRangeQuery, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	qp := r.URL.Query()

	trq.Statement = qp.Get(upQuery)
	if trq.Statement == "" {
		return nil, proxy.ErrorMissingURLParam(upQuery)
	}

	if p := qp.Get(upStart); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, err
		}
		trq.Extent.Start = t
	} else {
		return nil, proxy.ErrorMissingURLParam(upStart)
	}

	if p := qp.Get(upEnd); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, err
		}
		trq.Extent.End = t
	} else {
		return nil, proxy.ErrorMissingURLParam(upEnd)
	}

	if p := qp.Get(upStep); p != "" {
		step, err := parseDuration(p)
		if err != nil {
			return nil, err
		}
		trq.Step = step
	} else {
		return nil, proxy.ErrorMissingURLParam(upStep)
	}

	return trq, nil
}
