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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
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
	nativePlan, nativeOK := trq.ParsedQuery.(*model.QueryPlan)
	sqlPlan, sqlOK := trq.ParsedQuery.(*model.SQLQueryPlan)
	if (!nativeOK || nativePlan == nil) && (!sqlOK || sqlPlan == nil || sqlPlan.Plan == nil) {
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
	if nativeOK && nativePlan != nil {
		endExclusive := extent.End.Add(trq.Step)
		if !endExclusive.After(extent.End) || endExclusive.Year() < 1 || endExclusive.Year() > 9999 {
			c.observeRewriteFailure("invalid_extent")
			return errInvalidRewrite
		}
		body, err := nativePlan.RenderInterval(interval(extent.Start, endExclusive))
		if err != nil {
			c.observeRewriteFailure("render_error")
			return fmt.Errorf("%w: %w", errRenderQuery, err)
		}
		request.SetBody(r, body)
		return nil
	}

	// The SQL renderer owns comparator-preserving bound offsets. Re-encode the
	// original Druid SQL envelope so context, resultFormat, and any future
	// provider fields survive each extent rewrite unchanged.
	rendered, err := sqlPlan.Plan.RenderExtent(*extent)
	if err != nil {
		c.observeRewriteFailure("render_error")
		return fmt.Errorf("%w: %w", errRenderQuery, err)
	}
	body, err := c.sqlBody(r, trq, rendered)
	if err != nil {
		return err
	}
	// request.Clone's span rebinding intentionally shares Resources with the
	// client request. Split that reference before SetBody updates the buffered
	// body; otherwise the rendered extent would become part of the client's
	// cache-key input on the next DeriveCacheKey call.
	if rsc := request.GetResources(r); rsc != nil {
		*r = *request.SetResources(r, rsc.Clone())
	}
	request.SetBody(r, body)
	return nil
}

// FetchPartialBucket fetches one partial bucket of r's native or SQL query, rendered over the
// bucket's raw range, through the object proxy cache
func (c *Client) FetchPartialBucket(r *http.Request, trq *timeseries.TimeRangeQuery,
	pb timeseries.PartialBucket, _ bool,
) (timeseries.Timeseries, status.LookupStatus, error) {
	if r == nil || trq == nil {
		return nil, status.LookupStatusError, errInvalidRewrite
	}
	var body []byte
	var err error
	switch plan := trq.ParsedQuery.(type) {
	case *model.QueryPlan:
		// intervals are half-open and always closed
		if pb.Upper.IsZero() || pb.LowerExclusive || pb.UpperInclusive {
			return nil, status.LookupStatusError, backends.ErrPartialBucketsUnsupported
		}
		if body, err = plan.RenderInterval(interval(pb.Lower, pb.Upper)); err != nil {
			return nil, status.LookupStatusError, fmt.Errorf("%w: %w", errRenderQuery, err)
		}
	case *model.SQLQueryPlan:
		if plan == nil {
			return nil, status.LookupStatusError, errMissingQueryPlan
		}
		rendered, err := plan.Plan.RenderRange(pb)
		if err != nil {
			return nil, status.LookupStatusError, fmt.Errorf("%w: %w", errRenderQuery, err)
		}
		if body, err = c.sqlBody(r, trq, rendered); err != nil {
			return nil, status.LookupStatusError, err
		}
	default:
		return nil, status.LookupStatusError, errMissingQueryPlan
	}
	nr, err := request.Clone(r)
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	request.SetBody(nr, body)
	return engines.FetchPartialBucket(nr, nil, trq, c.Modeler())
}

func (c *Client) sqlBody(r *http.Request, trq *timeseries.TimeRangeQuery, rendered string) ([]byte, error) {
	// the original Druid SQL envelope with its statement replaced, so context, resultFormat and any
	// other provider fields reach the origin unchanged
	original := trq.OriginalBody
	var err error
	if len(original) == 0 {
		original, err = request.GetBody(r)
		if err != nil {
			c.observeRewriteFailure("body_error")
			return nil, fmt.Errorf("%w: %w", errRenderQuery, err)
		}
	}
	document, err := decodeJSONObject(original)
	if err != nil {
		c.observeRewriteFailure("body_error")
		return nil, fmt.Errorf("%w: %w", errRenderQuery, err)
	}
	document["query"] = rendered
	body, _, _, err := marshalJSONObject(document, nil)
	if err != nil {
		c.observeRewriteFailure("render_error")
		return nil, fmt.Errorf("%w: %w", errRenderQuery, err)
	}
	return body, nil
}

func interval(start, endExclusive time.Time) string {
	return start.UTC().Format(time.RFC3339Nano) + "/" + endExclusive.UTC().Format(time.RFC3339Nano)
}
