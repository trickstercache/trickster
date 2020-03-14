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

package prometheus

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Comcast/trickster/internal/proxy/urls"
	"github.com/Comcast/trickster/internal/timeseries"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {
	params := r.URL.Query()
	params.Set(upStart, strconv.FormatInt(extent.Start.Unix(), 10))
	params.Set(upEnd, strconv.FormatInt(extent.End.Unix(), 10))
	r.URL.RawQuery = params.Encode()
}

// FastForwardURL returns the url to fetch the Fast Forward value based on a timerange url
func (c *Client) FastForwardURL(r *http.Request) (*url.URL, error) {

	u := urls.Clone(r.URL)

	if strings.HasSuffix(u.Path, "/query_range") {
		u.Path = u.Path[0 : len(u.Path)-6]
	}

	p := u.Query()
	p.Del(upStart)
	p.Del(upEnd)
	p.Del(upStep)
	u.RawQuery = p.Encode()

	return u, nil
}
