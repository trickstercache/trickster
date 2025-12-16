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

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// SeriesHandler proxies requests for path /series to the origin by way of the object proxy cache
func (c *Client) SeriesHandler(w http.ResponseWriter, r *http.Request) {
	// if this request is part of a scatter/gather, provide a reconstitution function
	rsc := request.GetResources(r)
	if rsc != nil && rsc.IsMergeMember {
		rsc.MergeFunc = model.MergeAndWriteSeriesMergeFunc()
		rsc.MergeRespondFunc = model.MergeAndWriteSeriesRespondFunc()
	}

	u := urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	qp, _, _ := params.GetRequestValues(r)

	// Round Start and End times down to top of most recent minute for cacheability
	roundTimestampsToMinute(qp)

	r.URL = u
	params.SetRequestValues(r, qp)

	engines.ObjectProxyCacheRequest(w, r)
}
