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
	"net/http"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// needed to flag the object proxy cache when transformations are required
func indicateTransoformations(timeseries.Timeseries) {}

// QueryHandler handles calls to /query (for instantaneous values)
func (c *Client) QueryHandler(w http.ResponseWriter, r *http.Request) {
	var err error
	rsc := request.GetResources(r)

	// this checks if there are any labels to append, or whether it's part of a scatter/gather,
	// and if so, sets up the request context for these scenarios
	if rsc != nil {
		if rsc.IsMergeMember || (rsc.BackendOptions != nil && rsc.BackendOptions.Prometheus != nil) {
			var trq *timeseries.TimeRangeQuery
			trq, err = parseVectorQuery(r, c.instantRounder)
			if err == nil {
				rsc.TimeRangeQuery = trq
			}
			if rsc.IsMergeMember {
				m := c.Modeler()
				if m != nil {
					rsc.MergeFunc = model.MergeAndWriteVectorMergeFunc(m.WireUnmarshaler)
					rsc.MergeRespondFunc = model.MergeAndWriteVectorRespondFunc(m.WireMarshalWriter)
				}
			}
		}
		if c.hasTransformations {
			rsc.TSTransformer = indicateTransoformations
		}
	}

	u := urls.BuildUpstreamURL(r, c.BaseUpstreamURL())
	qp, _, _ := params.GetRequestValues(r)
	// Round time param down to the nearest 15 seconds if it exists
	if p := qp.Get(upTime); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(upTime, strconv.FormatInt(time.Unix(i, 0).Truncate(c.instantRounder).Unix(), 10))
		}
	}
	r.URL = u
	params.SetRequestValues(r, qp)

	// if there are labels to append to the dataset,
	if c.hasTransformations {
		// use a streaming response writer to capture the response body for transformation
		sw := &transformationResponseWriter{
			ResponseWriter: w,
			header:         make(http.Header),
			body:           make([]byte, 0),
		}
		engines.ObjectProxyCacheRequest(sw, r)
		statusCode := sw.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		if rsc != nil && rsc.Response != nil {
			statusCode = rsc.Response.StatusCode
		}
		c.processVectorTransformations(w, sw.body, statusCode, rsc)
		return
	}

	engines.ObjectProxyCacheRequest(w, r)
}

// transformationResponseWriter captures the response for transformations
type transformationResponseWriter struct {
	http.ResponseWriter
	header     http.Header
	statusCode int
	body       []byte
}

func (tw *transformationResponseWriter) Header() http.Header {
	return tw.header
}

func (tw *transformationResponseWriter) WriteHeader(code int) {
	tw.statusCode = code
}

func (tw *transformationResponseWriter) Write(b []byte) (int, error) {
	tw.body = append(tw.body, b...)
	return len(b), nil
}
