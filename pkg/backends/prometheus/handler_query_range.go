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

	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// QueryRangeHandler handles timeseries requests for
// Prometheus and processes them through the delta proxy cache
func (c *Client) QueryRangeHandler(w http.ResponseWriter, r *http.Request) {

	// if this request is part of a scatter/gather, provide a reconstitution function
	rsc := request.GetResources(r)
	if rsc != nil {
		if c.hasTransformations {
			rsc.TSTransformer = c.ProcessTransformations
		}
		if rsc.IsMergeMember {
			rsc.ResponseMergeFunc = merge.Timeseries
		}
	}
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}
