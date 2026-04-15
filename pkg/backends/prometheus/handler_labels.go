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
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// LabelsHandler proxies requests for path /label and /labels to the origin by way of the object proxy cache
func (c *Client) LabelsHandler(w http.ResponseWriter, r *http.Request) {
	origPath := r.URL.Path
	u := urls.BuildUpstreamURL(r, c.BaseUpstreamURL())

	rsc := request.GetResources(r)
	if rsc != nil && rsc.IsMergeMember {
		rsc.MergeFunc = model.MergeAndWriteLabelDataMergeFunc()
		rsc.MergeRespondFunc = model.MergeAndWriteLabelDataRespondFunc()
	}

	qp, _, _ := params.GetRequestValues(r)

	// start rounds down, end rounds up — see roundEndTimestampParameterToMinute
	roundTimestampsToMinute(qp)

	r.URL = u
	params.SetRequestValues(r, qp)

	if c.hasTransformations {
		sw := capture.NewCaptureResponseWriter()
		engines.ObjectProxyCacheRequest(sw, r)
		headers.Merge(w.Header(), sw.Header())
		body := c.processLabelsResponse(sw.Body(), origPath)
		w.Header().Del(headers.NameContentLength)
		w.Header().Del(headers.NameContentEncoding)
		w.WriteHeader(sw.StatusCode())
		w.Write(body)
		return
	}

	engines.ObjectProxyCacheRequest(w, r)
}
