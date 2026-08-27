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

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flux"
	ti "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

// Upstream Endpoints
const (
	mnQuery    = "query"
	apiv2Query = "api/v2/query"
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
	switch q := trq.ParsedQuery.(type) {
	case *influxql.Query:
		ti.SetExtent(r, trq, extent, q)
	case *flux.Query:
		flux.SetExtent(r, trq, extent, q)
	default:
		return fmt.Errorf("unsupported InfluxDB parsed query type %T", trq.ParsedQuery)
	}
	return nil
}
