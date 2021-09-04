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
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// LabelsHandler proxies requests for path /label and /labels to the origin by way of the object proxy cache
func (c *Client) LabelsHandler(w http.ResponseWriter, r *http.Request) {

	u := urls.BuildUpstreamURL(r, c.BaseUpstreamURL())

	rsc := request.GetResources(r)
	if rsc.IsMergeMember {
		rsc.ResponseMergeFunc = model.MergeAndWriteLabelData
	}

	qp, _, _ := params.GetRequestValues(r)

	// Round Start and End times down to top of most recent minute for cacheability
	if p := qp.Get(upStart); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(upStart, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(60)).Unix(), 10))
		}
	}

	if p := qp.Get(upEnd); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(upEnd, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(60)).Unix(), 10))
		}
	}

	r.URL = u
	params.SetRequestValues(r, qp)

	engines.ObjectProxyCacheRequest(w, r)
}
