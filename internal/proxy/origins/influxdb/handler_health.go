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

package influxdb

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/headers"
)

// HealthHandler checks the health of the Configured Upstream Origin
func (c *Client) HealthHandler(w http.ResponseWriter, r *http.Request) {

	if c.healthURL == nil {
		c.populateHeathCheckRequestValues()
	}

	if c.healthMethod == "-" {
		w.WriteHeader(400)
		w.Write([]byte("Health Check URL not Configured for origin: " + c.config.Name))
		return
	}

	req, _ := http.NewRequest(c.healthMethod, c.healthURL.String(), nil)
	req = req.WithContext(r.Context())

	req.Header = c.healthHeaders
	engines.DoProxy(w, req)
}

func (c *Client) populateHeathCheckRequestValues() {

	oc := c.config

	if oc.HealthCheckUpstreamPath == "-" {
		oc.HealthCheckUpstreamPath = "/"
	}
	if oc.HealthCheckVerb == "-" {
		oc.HealthCheckVerb = http.MethodGet
	}
	if oc.HealthCheckQuery == "-" {
		q := url.Values{"query": {oc.HealthCheckQuery}}
		oc.HealthCheckQuery = q.Encode()
	}

	c.healthURL = c.BaseURL()
	c.healthURL.Path += oc.HealthCheckUpstreamPath
	c.healthURL.RawQuery = oc.HealthCheckQuery
	c.healthMethod = oc.HealthCheckVerb

	if oc.HealthCheckHeaders != nil {
		c.healthHeaders = http.Header{}
		headers.UpdateHeaders(c.healthHeaders, oc.HealthCheckHeaders)
	}
}
