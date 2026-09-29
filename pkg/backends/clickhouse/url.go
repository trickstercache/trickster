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

package clickhouse

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Common URL Parameter Names
const (
	upQuery     = "query"
	upSessionID = "session_id"
)

var (
	errInvalidRewriteInput   = errors.New("invalid ClickHouse extent rewrite input")
	errMissingQueryPlan      = errors.New("ClickHouse query plan is missing")
	errInvalidRewriteRequest = errors.New("ClickHouse extent rewrite request has no URL")
)

// SetExtent changes the upstream request query to the provided cache-miss extent.
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	if extent == nil || r == nil || trq == nil {
		c.observeRewriteFailure("invalid_input")
		return errInvalidRewriteInput
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok {
		c.observeRewriteFailure("missing_plan")
		return errMissingQueryPlan
	}
	query, err := plan.RenderExtent(*extent)
	if err != nil {
		c.observeRewriteFailure("render_error")
		return fmt.Errorf("render ClickHouse extent: %w", err)
	}
	return c.setQuery(r, query)
}

// FetchPartialBucket fetches one partial bucket of r's query, rendered over the bucket's raw range,
// through the object proxy cache
func (c *Client) FetchPartialBucket(r *http.Request, trq *timeseries.TimeRangeQuery,
	pb timeseries.PartialBucket, _ bool,
) (timeseries.Timeseries, status.LookupStatus, error) {
	if r == nil || trq == nil {
		return nil, status.LookupStatusError, errInvalidRewriteInput
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok {
		return nil, status.LookupStatusError, errMissingQueryPlan
	}
	query, err := plan.RenderRange(pb)
	if err != nil {
		return nil, status.LookupStatusError, fmt.Errorf("render ClickHouse partial bucket: %w", err)
	}
	nr, err := request.Clone(r)
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	if err := c.setQuery(nr, query); err != nil {
		return nil, status.LookupStatusError, err
	}
	return engines.FetchPartialBucket(nr, nil, trq, c.Modeler())
}

func (c *Client) setQuery(r *http.Request, query string) error {
	if methods.HasBody(r.Method) {
		request.SetBody(r, []byte(query))
		return nil
	}
	if r.URL == nil {
		c.observeRewriteFailure("invalid_request")
		return errInvalidRewriteRequest
	}
	parameters := r.URL.Query()
	parameters.Set(upQuery, query)
	r.URL.RawQuery = parameters.Encode()
	return nil
}
