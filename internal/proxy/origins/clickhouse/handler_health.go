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

package clickhouse

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

const (
	healthQuery = "SELECT 1 FORMAT JSON"
)

var healthURL *url.URL
var healthMethod string
var originName string

// HealthHandler checks the health of the Configured Upstream Origin
func (c *Client) HealthHandler(w http.ResponseWriter, r *http.Request) {

	if healthURL == nil {
		cfg := c.Configuration()
		populateHeathCheckRequestValues(cfg)
		healthURL = c.BaseURL()
		originName = cfg.Name
		healthURL.Path += cfg.HealthCheckUpstreamPath
		healthURL.RawQuery = cfg.HealthCheckQuery
		healthMethod = cfg.HealthCheckVerb
	}

	if healthMethod == "-" {
		w.WriteHeader(400)
		w.Write([]byte("Health Check URL not Configured for origin: " + originName))
		return
	}

	engines.ProxyRequest(
		model.NewRequest("HealthHandler",
			healthMethod, healthURL, nil, c.config.Timeout, r, c.webClient), w)
}

func populateHeathCheckRequestValues(oc *config.OriginConfig) {
	if oc.HealthCheckUpstreamPath == "-" {
		oc.HealthCheckUpstreamPath = "/"
	}
	if oc.HealthCheckVerb == "-" {
		oc.HealthCheckVerb = http.MethodGet
	}
	if oc.HealthCheckQuery == "-" {
		q := url.Values{"query": {healthQuery}}
		oc.HealthCheckQuery = q.Encode()
	}
}
