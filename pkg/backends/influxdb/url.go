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
	"errors"
	"fmt"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	ti "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote"
	isql "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/sql"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

// Upstream Endpoints
const (
	mnQuery            = "query"
	apiv2Query         = "api/v2/query"
	apiv3QuerySQL      = "api/v3/query_sql"
	apiv3QueryInfluxQL = "api/v3/query_influxql"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	if r == nil || trq == nil || extent == nil {
		return errors.New("invalid InfluxDB extent rewrite input")
	}
	if trq.ParsedQuery == nil {
		t2, _, _, err := c.ParseTimeRangeQuery(r)
		if err != nil {
			return fmt.Errorf("parse InfluxDB query for extent rewrite: %w", err)
		}
		if t2 == nil {
			return errors.New("parse InfluxDB query for extent rewrite returned no query")
		}
		trq.ParsedQuery = t2.ParsedQuery
	}
	if promremote.IsParsedQuery(trq.ParsedQuery) {
		return promremote.SetExtent(r, trq, extent)
	}
	switch q := trq.ParsedQuery.(type) {
	case *isql.Query:
		isql.SetExtent(r, trq, extent, q)
	case *isql.V3InfluxQLQuery:
		if inner, ok := q.Inner.(*influxql.Query); ok {
			isql.SetExtentV3InfluxQL(r, trq, extent, inner)
		}
	case *influxql.Query:
		ti.SetExtent(r, trq, extent, q)
	case *flux.Query:
		flux.SetExtent(r, trq, extent, q)
	default:
		return fmt.Errorf("unsupported InfluxDB parsed query type %T", trq.ParsedQuery)
	}
	return nil
}

// FetchPartialBucket fetches one partial bucket of r's InfluxQL or SQL query, rendered over its raw
// range, through the object proxy cache; Flux and remote-read have none
func (c *Client) FetchPartialBucket(r *http.Request, trq *timeseries.TimeRangeQuery,
	pb timeseries.PartialBucket, _ bool,
) (timeseries.Timeseries, status.LookupStatus, error) {
	if r == nil || trq == nil {
		return nil, status.LookupStatusError, backends.ErrPartialBucketsUnsupported
	}
	var statement string
	var set func(*http.Request, string)
	var err error
	switch q := trq.ParsedQuery.(type) {
	case *isql.Query:
		statement, err = q.Plan.RenderRange(pb)
		set = isql.SetStatement
	case *isql.V3InfluxQLQuery:
		inner, ok := q.Inner.(*influxql.Query)
		if !ok {
			return nil, status.LookupStatusError, backends.ErrPartialBucketsUnsupported
		}
		statement, err = ti.RenderRange(inner, pb)
		set = isql.SetStatement
	case *influxql.Query:
		statement, err = ti.RenderRange(q, pb)
		set = ti.SetStatement
	default:
		return nil, status.LookupStatusError, backends.ErrPartialBucketsUnsupported
	}
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	nr, err := request.Clone(r)
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	set(nr, statement)
	return engines.FetchPartialBucket(nr, nil, trq, c.Modeler())
}
