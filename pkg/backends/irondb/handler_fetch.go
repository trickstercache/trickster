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
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
)

// FetchHandler handles requests for numeric timeseries data with specified
// spans and processes them through the delta proxy cache.
func (c *Client) FetchHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	engines.DeltaProxyCacheRequest(w, r, c.Modeler())
}

// fetchHandlerSetExtent will change the upstream request query to use the
// provided Extent.
func (c *Client) fetchHandlerSetExtent(r *http.Request,
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

	b, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}

	fetchReq := map[string]interface{}{}
	if err = json.NewDecoder(bytes.NewReader(b)).Decode(&fetchReq); err != nil {
		return
	}

	st := extent.Start.UnixNano() - (extent.Start.UnixNano() % int64(trq.Step))
	et := extent.End.UnixNano() - (extent.End.UnixNano() % int64(trq.Step))
	if st == et {
		et += int64(trq.Step)
	}

	ct := (et - st) / int64(trq.Step)
	fetchReq[rbStart] = time.Unix(0, st).Unix()
	fetchReq[rbCount] = ct
	newBody := &bytes.Buffer{}
	err = json.NewEncoder(newBody).Encode(&fetchReq)
	if err != nil {
		return
	}

	r.Body = io.NopCloser(newBody)
}

// fetchHandlerParseTimeRangeQuery parses the key parts of a TimeRangeQuery
// from the inbound HTTP Request.
func (c *Client) fetchHandlerParseTimeRangeQuery(
	r *http.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{}

	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, errors.ParseRequestBody(err)
	}

	r.Body = io.NopCloser(bytes.NewReader(b))
	fetchReq := map[string]interface{}{}
	if err = json.NewDecoder(bytes.NewReader(b)).Decode(&fetchReq); err != nil {
		return nil, errors.ParseRequestBody(err)
	}

	var i float64
	var ok bool
	if i, ok = fetchReq[rbStart].(float64); !ok {
		return nil, errors.MissingRequestParam(rbStart)
	}

	trq.Extent.Start = time.Unix(int64(i), 0)
	if i, ok = fetchReq[rbPeriod].(float64); !ok {
		return nil, errors.MissingRequestParam(rbPeriod)
	}

	trq.Step = time.Second * time.Duration(i)
	if i, ok = fetchReq[rbCount].(float64); !ok {
		return nil, errors.MissingRequestParam(rbCount)
	}

	trq.Extent.End = trq.Extent.Start.Add(trq.Step * time.Duration(i))
	return trq, nil
}

// fetchHandlerDeriveCacheKey calculates a query-specific keyname based on the
// user request.
func (c *Client) fetchHandlerDeriveCacheKey(path string, params url.Values,
	headers http.Header, body io.ReadCloser, extra string) (string, io.ReadCloser) {
	var sb strings.Builder
	sb.WriteString(path)
	newBody := &bytes.Buffer{}
	if b, err := io.ReadAll(body); err == nil {
		body = io.NopCloser(bytes.NewReader(b))
		fetchReq := map[string]interface{}{}
		err := json.NewDecoder(bytes.NewReader(b)).Decode(&fetchReq)
		if err == nil {
			delete(fetchReq, "start")
			delete(fetchReq, "end")
			delete(fetchReq, "count")
			err = json.NewEncoder(newBody).Encode(&fetchReq)
			if err == nil {
				sb.Write(newBody.Bytes())
			}
		}
	}

	sb.WriteString(extra)
	return md5.Checksum(sb.String()), body
}
