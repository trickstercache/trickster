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

	"github.com/tricksterproxy/trickster/pkg/proxy/engines"
	"github.com/tricksterproxy/trickster/pkg/proxy/params"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
)

// QueryHandler handles calls to /query (for instantaneous values)
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	u := urls.BuildUpstreamURL(r, c.baseUpstreamURL)
	qp, _, _ := params.GetRequestValues(r)
	// Round time param down to the nearest 15 seconds if it exists
	if p := qp.Get(upTime); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(upTime, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(15)).Unix(), 10))
		}
	}
	r.URL = u
	params.SetRequestValues(r, qp)

	engines.ObjectProxyCacheRequest(w, r)
}
