/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"net/url"
	"strings"

	"github.com/tricksterproxy/trickster/pkg/proxy/engines"
	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/params"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"github.com/tricksterproxy/trickster/pkg/util/regexp/matching"
	"github.com/tricksterproxy/trickster/pkg/util/timeconv"
)

// QueryHandler handles timeseries requests for InfluxDB and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	qp, _, _ := params.GetRequestValues(r)
	q := strings.Trim(gs.ToLower(qp.Get(upQuery)))
	if q == "" {
		c.ProxyHandler(w, r)
		return
	}
	// move past any semicolons
	for {
		if q[0] != ';' {
			break
		}
		q = q[1:]
	}
	// if it's not a select statement, just proxy it instead
	if !strings.HasPrefix(q, "select ") {
		c.ProxyHandler(w, r)
		return
	}

	r.URL = urls.BuildUpstreamURL(r, c.baseUpstreamURL)
	engines.DeltaProxyCacheRequest(w, r, c.modeler)
}

var epochToFlag = map[string]byte{
	"ns": 1,
	"u":  2, "µ": 2,
	"ms": 3,
	"s":  4,
	"m":  5,
	"h":  6,
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	rlo := &timeseries.RequestOptions{}

	v, _, _ := params.GetRequestValues(r)
	if trq.Statement = v.Get(upQuery); trq.Statement == "" {
		return nil, nil, false, errors.MissingURLParam(upQuery)
	}

	if b, ok := epochToFlag[v.Get(upEpoch)]; ok {
		rlo.TimeFormat = b
	}

	if v.Get(upPretty) == "true" {
		rlo.OutputFormat = 1
	} else if r != nil && r.Header != nil &&
		r.Header.Get(headers.NameAccept) == headers.ValueApplicationCSV {
		rlo.OutputFormat = 2
	}

	// if the Step wasn't found in the query (e.g., "group by time(1m)"), just proxy it instead
	step, found := matching.GetNamedMatch("step", reStep, trq.Statement)
	if !found {
		return nil, nil, false, errors.ErrStepParse
	}
	stepDuration, err := timeconv.ParseDuration(step)
	if err != nil {
		return nil, nil, false, errors.ErrStepParse
	}
	trq.Step = stepDuration
	trq.Statement, trq.Extent = getQueryParts(trq.Statement)
	trq.TemplateURL = urls.Clone(r.URL)

	qt := url.Values(http.Header(v).Clone())
	qt.Set(upQuery, trq.Statement)

	// Swap in the Tokenzed Query in the Url Params
	trq.TemplateURL.RawQuery = qt.Encode()

	return trq, rlo, false, nil

}
