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
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/irondb/common"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// CAQLHandler handles CAQL requests for timeseries data and processes them
// through the delta proxy cache.
func (c *Client) CAQLHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}

// caqlHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c *Client) caqlHandlerSetExtent(r *http.Request,
	trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent) {

	if r == nil || extent == nil || (extent.Start.IsZero() && extent.End.IsZero()) {
		return
	}

	var err error
	if trq == nil {
		if trq, _, _, err = c.ParseTimeRangeQuery(r); err != nil {
			return
		}
	}

	st := extent.Start.UnixNano() - (extent.Start.UnixNano() % int64(trq.Step))
	et := extent.End.UnixNano() - (extent.End.UnixNano() % int64(trq.Step))
	if st == et {
		et += int64(trq.Step)
	}

	q := r.URL.Query()
	q.Set(upCAQLStart, common.FormatTimestamp(time.Unix(0, st), false))
	q.Set(upCAQLEnd, common.FormatTimestamp(time.Unix(0, et), false))
	r.URL.RawQuery = q.Encode()
}

// caqlHandlerParseTimeRangeQuery parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) caqlHandlerParseTimeRangeQuery(
	r *http.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}
	trq.Statement = r.URL.Path

	qp := r.URL.Query()
	var err error
	p := ""

	if p = qp.Get(upQuery); p == "" {
		if p = qp.Get(upCAQLQuery); p == "" {
			return nil, errors.MissingURLParam(upQuery + " or " + upCAQLQuery)
		}
	}

	trq.Statement = p

	if p = qp.Get(upCAQLStart); p == "" {
		return nil, errors.MissingURLParam(upCAQLStart)
	}

	if trq.Extent.Start, err = common.ParseTimestamp(p); err != nil {
		return nil, err
	}

	if p = qp.Get(upCAQLEnd); p == "" {
		return nil, errors.MissingURLParam(upCAQLEnd)
	}

	if trq.Extent.End, err = common.ParseTimestamp(p); err != nil {
		return nil, err
	}

	if p = qp.Get(upCAQLPeriod); p == "" {
		return nil, errors.MissingURLParam(upCAQLPeriod)
	}

	if !strings.HasSuffix(p, "s") {
		p += "s"
	}

	if trq.Step, err = common.ParseDuration(p); err != nil {
		return nil, err
	}

	return trq, nil
}

// caqlHandlerFastForwardURL returns the url to fetch the Fast Forward value
// based on a timerange URL.
func (c *Client) caqlHandlerFastForwardRequest(
	r *http.Request) (*http.Request, error) {

	rsc := request.GetResources(r)
	trq := rsc.TimeRangeQuery

	nr := r.Clone(context.Background())
	v, _, _ := params.GetRequestValues(nr)
	var err error

	if trq == nil {
		trq, _, _, err = c.ParseTimeRangeQuery(r)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().Unix()
	start := now - (now % int64(trq.Step.Seconds()))
	end := start + int64(trq.Step.Seconds())
	v.Set(upCAQLStart, common.FormatTimestamp(time.Unix(start, 0), false))
	v.Set(upCAQLEnd, common.FormatTimestamp(time.Unix(end, 0), false))
	params.SetRequestValues(nr, v)

	return nr, nil
}
