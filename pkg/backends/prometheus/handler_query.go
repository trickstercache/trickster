/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/tricksterproxy/trickster/pkg/backends/prometheus/model"
	"github.com/tricksterproxy/trickster/pkg/proxy/engines"
	"github.com/tricksterproxy/trickster/pkg/proxy/params"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	"github.com/tricksterproxy/trickster/pkg/proxy/response/merge"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// QueryHandler handles calls to /query (for instantaneous values)
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	var err error
	var hasTransformations bool

	rsc := request.GetResources(r)
	wasMergeMember := rsc.IsMergeMember

	// this checks if there are any labels to append, or whether it's part of a scatter/gather,
	// and if so, sets up the request context for these scenarios
	if rsc.IsMergeMember || (rsc.BackendOptions != nil && rsc.BackendOptions.Prometheus != nil) {
		var trq *timeseries.TimeRangeQuery
		trq, err = parseVectorQuery(r)
		if err == nil {
			rsc.TimeRangeQuery = trq
			hasTransformations = len(trq.Labels) > 0
		}
		if rsc.IsMergeMember || hasTransformations {
			// if there are transformations (e.g. labels to insert, merging with other datasets),
			// this will enable those
			rsc.IsMergeMember = true
			rsc.ResponseMergeFunc = model.MergeAndWriteVector
		}
	}

	u := urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	qp, _, _ := params.GetRequestValues(r)
	// Round time param down to the nearest 15 seconds if it exists
	if p := qp.Get(upTime); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(upTime, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(15)).Unix(), 10))
		}
	}
	r.URL = u
	params.SetRequestValues(r, qp)

	// if there are labels to append to the dataset, and it's not part of a merge, then
	// it runs through the merge writer for processing. it doesn't merge with anything,
	// but the labels get appended and written to the wire
	if hasTransformations && !wasMergeMember {
		mg := merge.NewResponseGate(w, r, rsc)
		engines.ObjectProxyCacheRequest(mg, r)
		model.MergeAndWriteVector(w, r, merge.ResponseGates{mg})
		return
	}

	// otherwise, process as normal
	engines.ObjectProxyCacheRequest(w, r)
}
