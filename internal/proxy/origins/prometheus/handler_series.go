/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// SeriesHandler proxies requests for path /series to the origin by way of the object proxy cache
func (c *Client) SeriesHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)

	params := r.URL.Query()

	// Round Start and End times down to top of most recent minute for cacheability
	if p := params.Get(upStart); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			params.Set(upStart, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(60)).Unix(), 10))
		}
	}

	if p := params.Get(upEnd); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			params.Set(upEnd, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(60)).Unix(), 10))
		}
	}

	r.URL.RawQuery = params.Encode()

	engines.ObjectProxyCacheRequest(
		model.NewRequest(c.name, otPrometheus, "SeriesHandler", r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c, c.cache, c.cache.Configuration().ObjectTTL, false, false)
}
