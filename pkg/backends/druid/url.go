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

package druid

import (
	"fmt"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SetExtent renders one cache-miss extent as Druid's half-open interval.
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	if r == nil || trq == nil || extent == nil {
		c.observeRewriteFailure("invalid_input")
		return errInvalidRewrite
	}
	plan, ok := trq.ParsedQuery.(*model.QueryPlan)
	if !ok || plan == nil {
		c.observeRewriteFailure("missing_plan")
		return errMissingQueryPlan
	}
	if trq.Step <= 0 {
		c.observeRewriteFailure("invalid_step")
		return errInvalidQueryStep
	}
	if extent.Start.After(extent.End) || extent.Start.Year() < 1 || extent.Start.Year() > 9999 ||
		extent.End.Year() < 1 || extent.End.Year() > 9999 {
		c.observeRewriteFailure("invalid_extent")
		return errInvalidRewrite
	}
	endExclusive := extent.End.Add(trq.Step)
	if !endExclusive.After(extent.End) || endExclusive.Year() < 1 || endExclusive.Year() > 9999 {
		c.observeRewriteFailure("invalid_extent")
		return errInvalidRewrite
	}
	interval := extent.Start.UTC().Format(time.RFC3339Nano) + "/" +
		endExclusive.UTC().Format(time.RFC3339Nano)
	body, err := plan.RenderInterval(interval)
	if err != nil {
		c.observeRewriteFailure("render_error")
		return fmt.Errorf("%w: %w", errRenderQuery, err)
	}
	request.SetBody(r, body)
	return nil
}
