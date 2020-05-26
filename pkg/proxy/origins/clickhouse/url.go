/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// Common URL Parameter Names
const (
	upQuery = "query"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {

	if extent == nil || r == nil || trq == nil {
		return
	}

	p := r.URL.Query()
	q := trq.TemplateURL.Query().Get(upQuery)

	if q != "" {
		p.Set(upQuery, interpolateTimeQuery(q, extent, trq.Step))
	}

	r.URL.RawQuery = p.Encode()
}
