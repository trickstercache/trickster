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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, _ *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) error {
	v, _, _ := params.GetRequestValues(r)
	v.Set(upStart, formatTime(extent.Start))
	v.Set(upEnd, formatTime(extent.End))
	params.SetRequestValues(r, v)
	return nil
}

// FetchPartialBucket fetches a range query's live point as Fast Forward: an instant query at its
// end, via the object proxy cache. Instant points have no other partial bucket.
func (c *Client) FetchPartialBucket(r *http.Request, trq *timeseries.TimeRangeQuery,
	_ timeseries.PartialBucket, isLive bool,
) (*dataset.DataSet, status.LookupStatus, error) {
	if !isLive {
		return nil, status.LookupStatusError, backends.ErrPartialBucketsUnsupported
	}
	ffReq, err := fastForwardRequest(r)
	if err != nil {
		return nil, status.LookupStatusError, err
	}
	return engines.FetchPartialBucket(ffReq, c.Configuration().FastForwardPath, trq, c.Modeler())
}

func fastForwardRequest(r *http.Request) (*http.Request, error) {
	// the range query's own request, as an instant query at its end
	nr, err := request.Clone(r)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(nr.URL.Path, "/query_range") {
		nr.URL.Path = nr.URL.Path[0 : len(nr.URL.Path)-6]
	}
	v, _, _ := params.GetRequestValues(nr)
	evaluationTime := v.Get(upEnd)
	v.Del(upStart)
	v.Del(upEnd)
	v.Del(upStep)
	if evaluationTime != "" {
		v.Set(upTime, evaluationTime)
	}
	params.SetRequestValues(nr, v)
	return nr, nil
}
