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
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {
	v, _, _ := params.GetRequestValues(r)
	v.Set(upStart, strconv.FormatInt(extent.Start.Unix(), 10))
	v.Set(upEnd, strconv.FormatInt(extent.End.Unix(), 10))
	params.SetRequestValues(r, v)
}

// FastForwardRequest returns an *http.Request crafted to collect Fast Forward
// data from the Origin, based on the provided HTTP Request
func (c *Client) FastForwardRequest(r *http.Request) (*http.Request, error) {
	nr := r.Clone(context.Background())
	if strings.HasSuffix(nr.URL.Path, "/query_range") {
		nr.URL.Path = nr.URL.Path[0 : len(nr.URL.Path)-6]
	}
	v, _, _ := params.GetRequestValues(nr)
	v.Del(upStart)
	v.Del(upEnd)
	v.Del(upStep)
	params.SetRequestValues(nr, v)
	return nr, nil
}
