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

package sql

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SetExtent renders the parsed query for the provided cache-miss extent and
// applies it to the upstream request.
func SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent, q *Query,
) {
	if extent == nil || r == nil || trq == nil || q == nil || q.Plan == nil {
		return
	}
	stmt, err := q.Plan.RenderExtent(*extent)
	if err != nil {
		return
	}
	if methods.HasBody(r.Method) {
		request.SetBody(r, EncodeBody(r, stmt))
		return
	}
	qi := r.URL.Query()
	qi.Set(ParamQuery, stmt)
	r.URL.RawQuery = qi.Encode()
}
