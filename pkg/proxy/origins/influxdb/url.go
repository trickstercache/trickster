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

package influxdb

import (
	"net/http"

	"github.com/trickstercache/trickster/pkg/proxy/params"
	"github.com/trickstercache/trickster/pkg/timeseries"
)

// Upstream Endpoints
const (
	mnQuery = "query"
)

// Common URL Parameter Names
const (
	upQuery = "q"
	upDB    = "db"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {
	v, _, _ := params.GetRequestValues(r)
	// the TemplateURL in the TimeRangeQuery will always have URL Query Params, even for POSTs
	// For POST, ParseTimeRangeQuery extracts the params from the original request body and
	// for use as the raw query string of the template URL, this facilitates
	// param manipulation, such as the below interpolation call, before forwarding
	t := trq.TemplateURL.Query()
	q := t.Get(upQuery)
	if q != "" {
		v.Set(upQuery, interpolateTimeQuery(q, trq, extent))
	}
	params.SetRequestValues(r, v)
}
