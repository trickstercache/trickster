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

package prometheus

import (
	"net/http"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, _ *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	v, _, _ := params.GetRequestValues(r)
	v.Set(upStart, formatTime(extent.Start))
	v.Set(upEnd, formatTime(extent.End))
	if c.hooks.PreserveQueryGrid {
		v.Set(upStart, extent.Start.UTC().Format(time.RFC3339Nano))
		v.Set(upEnd, extent.End.UTC().Format(time.RFC3339Nano))
	}
	params.SetRequestValues(r, v)
	return nil
}

// FetchPartialBucket fetches a range query's live point as Fast Forward: an instant query at its
// end, via the object proxy cache. Instant points have no other partial bucket.
func (c *Client) FetchPartialBucket(r *http.Request, trq *timeseries.TimeRangeQuery,
	_ timeseries.PartialBucket, isLive bool,
) (timeseries.Timeseries, status.LookupStatus, error) {
	if !isLive {
		return nil, status.LookupStatusError, backends.ErrPartialBucketsUnsupported
	}
	setFastForward(r)
	return engines.FetchPartialBucket(r, c.Configuration().FastForwardPath, trq, c.Modeler())
}

func setFastForward(r *http.Request) {
	// the range query's own request, as an instant query at its end
	if strings.HasSuffix(r.URL.Path, "/query_range") {
		r.URL.Path = r.URL.Path[0 : len(r.URL.Path)-6]
	}
	v, _, _ := params.GetRequestValues(r)
	evaluationTime := v.Get(upEnd)
	v.Del(upStart)
	v.Del(upEnd)
	v.Del(upStep)
	if evaluationTime != "" {
		v.Set(upTime, evaluationTime)
	}
	params.SetRequestValues(r, v)
}
