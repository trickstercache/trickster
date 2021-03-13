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
	"time"

	"github.com/tricksterproxy/trickster/pkg/proxy/engines"
	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/params"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
	"github.com/tricksterproxy/trickster/pkg/timeseries"

	"github.com/influxdata/influxql"
)

// QueryHandler handles timeseries requests for InfluxDB and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	qp, _, _ := params.GetRequestValues(r)
	q := strings.Trim(strings.ToLower(qp.Get(upQuery)), " \t\n")
	if q == "" {
		c.ProxyHandler(w, r)
		return
	}

	// if it's not a select statement, just proxy it instead
	if strings.Index(q, "select ") == -1 {
		c.ProxyHandler(w, r)
		return
	}

	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
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

	p := influxql.NewParser(strings.NewReader(trq.Statement))
	q, err := p.ParseQuery()
	if err != nil {
		return nil, nil, false, err
	}

	var hasTimeQueryParts bool
	for _, v := range q.Statements {
		if sel, ok := v.(*influxql.SelectStatement); ok {
			if sel.Condition != nil {
				step, err := sel.GroupByInterval()
				if err != nil {
					return nil, nil, false, err
				}
				trq.Step = step
				_, tr, err := influxql.ConditionExpr(sel.Condition, valuer)
				if err != nil {
					return nil, nil, false, err
				}
				trq.Extent = timeseries.Extent{Start: tr.Min, End: tr.Max}
				if trq.Extent.End.IsZero() {
					trq.Extent.End = time.Now()
				}

				// this sets a zero time range for normalizing the query for cache key hashing
				sel.SetTimeRange(time.Time{}, time.Time{})
				trq.Statement = sel.String()
				hasTimeQueryParts = true
				break
			}
		}
	}

	if !hasTimeQueryParts {
		return nil, nil, false, errors.ErrNotTimeRangeQuery
	}

	trq.ParsedQuery = q
	trq.TemplateURL = urls.Clone(r.URL)
	qt := url.Values(http.Header(v).Clone())
	qt.Set(upQuery, trq.Statement)

	// Swap in the Tokenzed Query in the Url Params
	trq.TemplateURL.RawQuery = qt.Encode()

	return trq, rlo, false, nil

}
