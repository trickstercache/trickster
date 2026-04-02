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
	"bytes"
	"compress/gzip"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func (c *Client) ProcessTransformations(ts timeseries.Timeseries) {
	if !c.hasTransformations {
		return
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return
	}
	ds.InjectTags(c.injectLabels)
}

func (c *Client) processVectorTransformations(w http.ResponseWriter,
	body []byte, statusCode int, rsc *request.Resources,
) {
	// Decompress gzip if the response body is gzip-encoded.
	// This can happen when ALB mechanisms (FGR, NLM, TSM) capture responses
	// from pool members that return compressed data.
	body = decompressGzip(body)
	var trq *timeseries.TimeRangeQuery
	if rsc != nil && rsc.TimeRangeQuery != nil {
		trq = rsc.TimeRangeQuery
	}
	t2, err := model.UnmarshalTimeseries(body, trq)
	if err != nil || t2 == nil {
		logger.Error("vector unmarshaling error",
			logging.Pairs{"provider": providers.Prometheus, "detail": err.Error()})
		defaultWrite(statusCode, w, body)
		return
	}
	ds := t2.(*dataset.DataSet) // failure of this type assertion should be impossible
	ds.InjectTags(c.injectLabels)
	var requestOptions *timeseries.RequestOptions
	if rsc != nil {
		requestOptions = rsc.TSReqestOptions
		rsc.TS = t2
	}
	model.MarshalTSOrVectorWriter(ds, requestOptions, statusCode, w, true)
}

func defaultWrite(statusCode int, w http.ResponseWriter, b []byte) {
	w.WriteHeader(statusCode)
	w.Write(b)
}

// decompressGzip returns decompressed bytes if b is gzip-encoded, otherwise returns b unchanged.
func decompressGzip(b []byte) []byte {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b
	}
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	defer gr.Close()
	decompressed, err := io.ReadAll(gr)
	if err != nil {
		return b
	}
	return decompressed
}
