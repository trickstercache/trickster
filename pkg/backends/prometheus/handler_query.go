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
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// vectorInstantMarshalWriter is a MarshalWriterFunc that forces vector
// (instant query) output shape. It lives alongside the other prometheus
// marshaler helpers in this file because it is only consumed by
// QueryHandler's strategy-aware merge RespondFunc — the generic
// WireMarshalWriter always emits matrix, which is wrong for /api/v1/query.
func vectorInstantMarshalWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	return model.MarshalTSOrVectorWriter(ts, rlo, status, w, true)
}

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
					if rsc.TSMergeStrategy != 0 {
						rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(m.WireUnmarshaler, rsc.TSMergeStrategy)
						// Instant queries marshal as vector, not matrix
						// (WireMarshalWriter always emits matrix shape).
						rsc.MergeRespondFunc = merge.TimeseriesRespondFuncWithStrategy(vectorInstantMarshalWriter, nil, rsc.TSMergeStrategy)
					} else {
						rsc.MergeFunc = model.MergeAndWriteVectorMergeFunc(m.WireUnmarshaler)
						rsc.MergeRespondFunc = model.MergeAndWriteVectorRespondFunc(m.WireMarshalWriter)
					}
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

	// if there are labels to append to the dataset, or if this is a merge member,
	// we need to capture and unmarshal the response
	if c.hasTransformations || (rsc != nil && rsc.IsMergeMember) {
		// use a streaming response writer to capture the response body for transformation
		sw := capture.NewCaptureResponseWriter()
		engines.ObjectProxyCacheRequest(sw, r)
		// Propagate captured upstream headers (Content-Type, X-Trickster-Result,
		// etc.) to the outer ResponseWriter. Without this, ALB mechanisms see a
		// body with no Content-Type and downstream consumers (Grafana, Mimir)
		// may discard it as empty. See trickstercache/trickster#937.
		headers.Merge(w.Header(), sw.Header())
		statusCode := sw.StatusCode()
		if rsc != nil && rsc.Response != nil {
			statusCode = rsc.Response.StatusCode
		}
		c.processVectorTransformations(w, sw.Body(), statusCode, rsc)
		return
	}

	engines.ObjectProxyCacheRequest(w, r)
}
