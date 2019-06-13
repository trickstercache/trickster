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

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// HealthHandler checks the health of the Configured Upstream Origin
func (c *Client) HealthHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BaseURL()

	cfg := c.Configuration()
	if cfg.HealthCheckUpstreamPath == "/" {
		u.Path += APIPath + "query"
		u.RawQuery = "query=up"
		r.Method = cfg.HealthCheckVerb
	} else {
		u.Path += cfg.HealthCheckUpstreamPath
		u.RawQuery = cfg.HealthCheckQuery
	}

	engines.ProxyRequest(model.NewRequest(c.name, otPrometheus, "HealthHandler", u, r.Header, c.config.Timeout, r, c.webClient), w)
}
