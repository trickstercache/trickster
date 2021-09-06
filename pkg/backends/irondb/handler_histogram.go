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
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/irondb/common"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
)

// HistogramHandler handles requests for historgam timeseries data and processes
// them through the delta proxy cache.
func (c *Client) HistogramHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}

// histogramHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c *Client) histogramHandlerSetExtent(r *http.Request,
	trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent) {
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

	ps := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 6)
	if len(ps) < 6 || ps[0] != "histogram" {
		return
	}

	sb := new(strings.Builder)
	if strings.HasPrefix(r.URL.Path, "/") {
		sb.WriteString("/")
	}

	sb.WriteString("histogram")
	sb.WriteString("/" + strconv.FormatInt(time.Unix(0, st).Unix(), 10))
	sb.WriteString("/" + strconv.FormatInt(time.Unix(0, et).Unix(), 10))
	sb.WriteString("/" + strings.Join(ps[3:], "/"))
	r.URL.Path = sb.String()
}

// histogramHandlerParseTimeRangeQuerycommon.Parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) histogramHandlerParseTimeRangeQuery(
	r *http.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}
	var ps []string
	if strings.HasPrefix(r.URL.Path, "/irondb") {
		ps = strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 7)
		if len(ps) > 0 {
			ps = ps[1:]
		}
	} else {
		ps = strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 6)
	}

	if len(ps) < 6 || ps[0] != "histogram" {
		return nil, errors.ErrNotTimeRangeQuery
	}

	trq.Statement = "/histogram/" + strings.Join(ps[4:], "/")

	var err error
	if trq.Extent.Start, err = common.ParseTimestamp(ps[1]); err != nil {
		return nil, err
	}

	if trq.Extent.End, err = common.ParseTimestamp(ps[2]); err != nil {
		return nil, err
	}

	if trq.Step, err = common.ParseDuration(ps[3]); err != nil {
		return nil, err
	}

	return trq, nil
}

// histogramHandlerDeriveCacheKey calculates a query-specific keyname based on
// the user request.
func (c *Client) histogramHandlerDeriveCacheKey(path string, params url.Values,
	headers http.Header, body io.ReadCloser, extra string) (string, io.ReadCloser) {
	var sb strings.Builder
	sb.WriteString(path)
	var ps []string
	if strings.HasPrefix(path, "/irondb") {
		ps = strings.SplitN(strings.TrimPrefix(path, "/"), "/", 7)
		if len(ps) > 0 {
			ps = ps[1:]
		}
	} else {
		ps = strings.SplitN(strings.TrimPrefix(path, "/"), "/", 6)
	}

	if len(ps) >= 6 || ps[0] == "histogram" {
		sb.WriteString("/histogram/" + strings.Join(ps[3:], "/"))
	}

	sb.WriteString(extra)
	return md5.Checksum(sb.String()), body
}

// histogramHandlerFastForwardURL returns the url to fetch the Fast Forward value
// based on a timerange URL.
func (c *Client) histogramHandlerFastForwardRequest(
	r *http.Request) (*http.Request, error) {

	rsc := request.GetResources(r)
	trq := rsc.TimeRangeQuery

	var err error
	nr := r.Clone(context.Background())
	u := nr.URL
	if trq == nil {
		trq, _, _, err = c.ParseTimeRangeQuery(r)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().Unix()
	start := now - (now % int64(trq.Step.Seconds()))
	end := start + int64(trq.Step.Seconds())
	ps := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 6)
	if len(ps) < 6 || ps[0] != "histogram" {
		return nil, errors.InvalidPath(u.Path)
	}

	sb := new(strings.Builder)
	if strings.HasPrefix(u.Path, "/") {
		sb.WriteString("/")
	}

	sb.WriteString("histogram")
	sb.WriteString("/" + strconv.FormatInt(time.Unix(start, 0).Unix(), 10))
	sb.WriteString("/" + strconv.FormatInt(time.Unix(end, 0).Unix(), 10))
	sb.WriteString("/" + strings.Join(ps[3:], "/"))
	u.Path = sb.String()

	return nr, nil
}
