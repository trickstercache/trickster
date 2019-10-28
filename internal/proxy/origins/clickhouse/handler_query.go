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

package clickhouse

import (
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	tt "github.com/Comcast/trickster/internal/proxy/timeconv"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/regexp/matching"
)

// QueryHandler handles timeseries requests for ClickHouse and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	rqlc := strings.Replace(strings.ToLower(r.URL.RawQuery), "%20", "+", -1)
	// if it's not a select statement, just proxy it instead
	if (!strings.HasPrefix(rqlc, "query=select+")) && (!(strings.Index(rqlc, "&query=select+") > 0)) {
		c.ProxyHandler(w, r)
		return
	}

	u := c.BuildUpstreamURL(r)
	engines.DeltaProxyCacheRequest(
		model.NewRequest("QueryHandler", r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c)

}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *model.Request) (*timeseries.TimeRangeQuery, error) {

	var ok bool

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	qi := r.TemplateURL.Query()
	if p, ok := qi[upQuery]; ok {
		trq.Statement = p[0]
	} else {
		return nil, errors.MissingURLParam(upQuery)
	}

	// if the Step wasn't found in the query (e.g., "group by time(1m)"), just proxy it instead
	found := matching.GetNamedMatches(reTimeFieldAndStep, trq.Statement, []string{"step", "timeField"})
	step, ok := found["step"]
	if !ok {
		return nil, errors.StepParse()
	}

	timeField, ok := found["timeField"]
	if !ok {
		return nil, errors.StepParse()
	}
	trq.TimestampFieldName = timeField

	stepDuration, err := tt.ParseDuration(step + "s")
	if err != nil {
		return nil, errors.StepParse()
	}
	trq.Step = stepDuration

	trq.Statement, trq.Extent, _, err = getQueryParts(trq.Statement, timeField)

	// Swap in the Tokenzed Query in the Url Params
	qi.Set(upQuery, trq.Statement)
	r.TemplateURL.RawQuery = qi.Encode()
	return trq, nil
}
