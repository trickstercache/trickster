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

package clickhouse

import (
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// QueryHandler handles timeseries requests for ClickHouse and processes them through the delta proxy cache
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {
	var sqlQuery string
	if methods.HasBody(r.Method) {
		b, err := request.GetBody(r)
		if err != nil {
			handlers.HandleBadRequestResponse(w, r)
			return
		}
		sqlQuery = string(b)
	} else {
		sqlQuery = r.URL.Query().Get(upQuery)
	}
	sqlQuery = strings.ToLower(sqlQuery)
	if !strings.Contains(sqlQuery, "select ") &&
		!strings.Contains(sqlQuery, "select\n") &&
		!strings.Contains(sqlQuery, "select\t") {
		logger.Debug("request is not a SELECT query, proxying.", logging.Pairs{"query": sqlQuery})
		c.ProxyHandler(w, r)
		return
	}
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}
