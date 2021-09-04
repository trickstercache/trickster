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

package irondb

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/irondb/common"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// RawHandler handles requests for raw numeric timeseries data and processes
// them through the delta proxy cache.
func (c *Client) RawHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}

// rawHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c *Client) rawHandlerSetExtent(r *http.Request,
	trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent) {
	q := r.URL.Query()
	q.Set(upStart, common.FormatTimestamp(extent.Start, true))
	q.Set(upEnd, common.FormatTimestamp(extent.End, true))
	r.URL.RawQuery = q.Encode()
}

// rawHandlerParseTimeRangeQuerycommon.Parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) rawHandlerParseTimeRangeQuery(
	r *http.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}
	trq.Statement = r.URL.Path

	qp := r.URL.Query()
	var err error
	p := ""
	if p = qp.Get(upStart); p == "" {
		return nil, errors.MissingURLParam(upStart)
	}

	if trq.Extent.Start, err = common.ParseTimestamp(p); err != nil {
		return nil, err
	}

	if p = qp.Get(upEnd); p == "" {
		return nil, errors.MissingURLParam(upEnd)
	}

	if trq.Extent.End, err = common.ParseTimestamp(p); err != nil {
		return nil, err
	}

	return trq, nil
}
