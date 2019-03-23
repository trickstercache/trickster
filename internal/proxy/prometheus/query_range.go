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
	"strconv"

	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/timeseries"
)

// QueryRangeHandler ...
func (c *Client) QueryRangeHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	proxy.DeltaProxyCacheRequest(proxy.NewRequest(c.Name, otPrometheus, "QueryRangeHandler", r.Method, u, r.Header, c.Config.Timeout, r), w, c, c.Cache, c.Cache.Configuration().RecordTTLSecs, false)
}

// ParseTimeRangeQuery ...
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	qp := r.URL.Query()

	if p, ok := qp[upQuery]; ok {
		trq.Statement = p[0]
	} else {
		return nil, proxy.ErrorMissingURLParam(upQuery)
	}

	if p, ok := qp[upStart]; ok {
		t, err := parseTime(p[0])
		if err != nil {
			return nil, err
		}
		trq.Extent.Start = t
	} else {
		return nil, proxy.ErrorMissingURLParam(upStart)
	}

	if p, ok := qp[upEnd]; ok {
		t, err := parseTime(p[0])
		if err != nil {
			return nil, err
		}
		trq.Extent.End = t
	} else {
		return nil, proxy.ErrorMissingURLParam(upEnd)
	}

	if p, ok := qp[upStep]; ok {
		step, err := strconv.ParseInt(p[0], 10, 32)
		if err != nil {
			return nil, err
		}
		trq.Step = step
	} else {
		return nil, proxy.ErrorMissingURLParam(upStep)
	}

	return trq, nil
}
