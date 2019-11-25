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
	"strings"

	"github.com/Comcast/trickster/internal/proxy/engines"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// QueryHandler handles timeseries requests for ClickHouse and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {

	rqlc := strings.Replace(strings.ToLower(r.URL.RawQuery), "%20", "+", -1)
	// if it's not a select statement, just proxy it instead
	if (!strings.HasPrefix(rqlc, "query=select+")) && (!(strings.Index(rqlc, "&query=select+") > 0)) &&
		(!strings.HasSuffix(rqlc, "format+json")) {
		c.ProxyHandler(w, r)
		return
	}

	u := c.BuildUpstreamURL(r)
	engines.DeltaProxyCacheRequest(
		model.NewRequest("QueryHandler", r.Method, u, r.Header, c.config.Timeout, r, c.webClient),
		w, c)

}
