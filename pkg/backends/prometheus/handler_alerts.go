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
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// AlertsHandler proxies requests for path /alerts to the origin by way of the object proxy cache
func (c *Client) AlertsHandler(w http.ResponseWriter, r *http.Request) {

	rsc := request.GetResources(r)
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	resp := engines.DoProxy(w, r, true)
	if rsc != nil && rsc.IsMergeMember {
		rsc.ResponseMergeFunc = model.MergeAndWriteAlerts
		rsc.Response = resp
	}

}
