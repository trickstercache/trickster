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

	"github.com/influxdata/influxql"
	"github.com/trickstercache/trickster/pkg/proxy/headers"
	"github.com/trickstercache/trickster/pkg/proxy/params"
	"github.com/trickstercache/trickster/pkg/timeseries"
)

// Upstream Endpoints
const (
	mnQuery = "query"
)

// Common URL Parameter Names
const (
	upQuery   = "q"
	upDB      = "db"
	upEpoch   = "epoch"
	upPretty  = "pretty"
	upChunked = "chunked"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {

	v, _, _ := params.GetRequestValues(r)
	if trq.ParsedQuery == nil {
		t2, _, _, err := c.ParseTimeRangeQuery(r)
		if err != nil {
			return
		}
		trq.ParsedQuery = t2.ParsedQuery
	}

	q, ok := trq.ParsedQuery.(*influxql.Query)
	if !ok {
		return
	}

	for _, s := range q.Statements {
		if sel, ok := s.(*influxql.SelectStatement); ok {
			// since setting timerange results in a clause of '>= start AND < end', we add the
			// size of 1 step onto the end time so as to ensure it is included in the results
			sel.SetTimeRange(extent.Start, extent.End.Add(trq.Step))
		}
	}

	v.Set(upQuery, q.String())
	v.Set(upEpoch, "ns") // request nanosecond epoch timestamp format from server
	v.Del(upChunked)     // we do not support chunked output or handling chunked server responses
	v.Del(upPretty)
	r.Header.Set(headers.NameAccept, headers.ValueApplicationJSON)
	params.SetRequestValues(r, v)
}
