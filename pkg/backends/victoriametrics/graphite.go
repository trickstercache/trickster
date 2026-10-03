/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

const (
	gpFrom  = "from"
	gpUntil = "until"
)

// GraphiteHandler serves VictoriaMetrics' Graphite read APIs from the Graphite base path. Responses
// are cached only as exact requests: a render is consolidated to maxDataPoints over its whole range,
// so ranges fetched apart can't be merged, and a render that reaches the volatile window is relayed.
func (c *Client) GraphiteHandler(w http.ResponseWriter, r *http.Request) {
	cacheable := c.prepareGraphite(r)
	c.toGraphiteUpstream(r)
	if !cacheable {
		engines.DoProxy(w, r, true)
		return
	}
	engines.ObjectProxyCacheRequest(w, r)
}

// GraphiteProxyHandler relays Graphite API requests, such as tag registration, uncached.
func (c *Client) GraphiteProxyHandler(w http.ResponseWriter, r *http.Request) {
	c.toGraphiteUpstream(r)
	engines.DoProxy(w, r, true)
}

func (c *Client) toGraphiteUpstream(r *http.Request) {
	r.URL.Path = graphiteSuffix(r.URL.Path)
	r.URL = urls.BuildUpstreamURL(r, c.GraphiteBaseURL())
}

// prepareGraphite reports whether a Graphite read can be cached: a GET, or a POST form whose fields
// don't repeat URL parameters, with absolute from and until times, and an until that has settled.
func (c *Client) prepareGraphite(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return false
	}
	if rsc := request.GetResources(r); rsc != nil && rsc.PathConfig != nil && len(rsc.PathConfig.RequestParams) != 0 {
		return false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	if r.Method == http.MethodPost {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/x-www-form-urlencoded" {
			return false
		}
		body, err := request.GetBody(r)
		if err != nil {
			return false
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return false
		}
		for name, fields := range form {
			if _, exists := values[name]; exists {
				return false
			}
			values[name] = fields
		}
	}
	isRender := strings.HasSuffix(r.URL.Path, "/render")
	for _, name := range []string{gpFrom, gpUntil} {
		v, ok := values[name]
		if !ok {
			if isRender {
				// render reads an absent range relative to now
				return false
			}
			continue
		}
		if len(v) != 1 || !epochSeconds(v[0]) {
			return false
		}
	}
	if isRender {
		until, _ := strconv.ParseInt(values.Get(gpUntil), 10, 64)
		if time.Since(time.Unix(until, 0)) < c.volatileWindow() {
			return false
		}
	}
	// the request is sent as it came: rewritten into both the URL and the body, as a POST form is
	// re-encoded, a repeated field such as target would be read twice
	return true
}

// epochSeconds reports whether v is a whole Unix timestamp in seconds, which Graphite's other time
// forms, such as -1h or now, are not.
func epochSeconds(v string) bool {
	return len(v) >= 9 && len(v) <= 10 && isDigits(v)
}
