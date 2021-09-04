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
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// needed to flag the object proxy cache when transformations are required
func indicateTransoformations(timeseries.Timeseries) {}

// QueryHandler handles calls to /query (for instantaneous values)
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	var err error
	rsc := request.GetResources(r)

	// this checks if there are any labels to append, or whether it's part of a scatter/gather,
	// and if so, sets up the request context for these scenarios
	if rsc != nil {
		if rsc.IsMergeMember || (rsc.BackendOptions != nil && rsc.BackendOptions.Prometheus != nil) {
			var trq *timeseries.TimeRangeQuery
			trq, err = parseVectorQuery(r, c.instantRounder)
			if err == nil {
				rsc.TimeRangeQuery = trq
			}
			if rsc.IsMergeMember {
				rsc.ResponseMergeFunc = model.MergeAndWriteVector
			}
		}
		if c.hasTransformations {
			rsc.TSTransformer = indicateTransoformations
		}
	}

	u := urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	qp, _, _ := params.GetRequestValues(r)
	// Round time param down to the nearest 15 seconds if it exists
	if p := qp.Get(upTime); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(upTime, strconv.FormatInt(time.Unix(i, 0).Truncate(c.instantRounder).Unix(), 10))
		}
	}
	r.URL = u
	params.SetRequestValues(r, qp)

	// if there are labels to append to the dataset,
	if c.hasTransformations {
		// using a merge response gate allows the capturing of the response body for transformation
		mg := merge.NewResponseGate(w, r, rsc)
		engines.ObjectProxyCacheRequest(mg, r)
		mg.Response = rsc.Response
		c.processVectorTransformations(w, mg)
		return
	}

	engines.ObjectProxyCacheRequest(w, r)
}
