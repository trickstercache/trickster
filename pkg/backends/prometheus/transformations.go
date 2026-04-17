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
	"encoding/json"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tgzip "github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
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
	wasGzip := len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
	body = tgzip.Decompress(body)
	if wasGzip {
		// We're about to write the decompressed bytes (or, if unmarshal succeeds,
		// a freshly marshaled JSON envelope). Either way the downstream payload is
		// no longer gzip-encoded, so any propagated upstream Content-Encoding /
		// Content-Length headers are now stale and would mislead the client.
		// See trickstercache/trickster#937.
		w.Header().Del(headers.NameContentEncoding)
		w.Header().Del(headers.NameContentLength)
	}
	var trq *timeseries.TimeRangeQuery
	if rsc != nil && rsc.TimeRangeQuery != nil {
		trq = rsc.TimeRangeQuery
	}
	t2, err := model.UnmarshalTimeseries(body, trq)
	if err != nil || t2 == nil {
		detail := "nil timeseries"
		if err != nil {
			detail = err.Error()
		}
		logger.Error("vector unmarshaling error",
			logging.Pairs{"provider": providers.Prometheus, "detail": detail, "body": string(body)})
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
	// Marshaled body length is computed by the writer; drop any stale length
	// header that came from the captured upstream response.
	w.Header().Del(headers.NameContentLength)
	model.MarshalTSOrVectorWriter(ds, requestOptions, statusCode, w, true)
}

// processLabelsResponse injects the backend's configured prometheus.labels
// into /api/v1/labels and /api/v1/label/<name>/values responses so that
// downstream TSM merges can surface them. For /labels it appends each
// configured key; for /label/<name>/values it appends the configured value
// only when <name> matches an injected key.
func (c *Client) processLabelsResponse(body []byte, path string) []byte {
	if !c.hasTransformations || len(body) == 0 {
		return body
	}
	decoded := tgzip.Decompress(body)
	var ld model.WFLabelData
	if err := json.Unmarshal(decoded, &ld); err != nil || ld.Envelope == nil ||
		ld.Envelope.Status != "success" {
		return body
	}
	seen := make(map[string]struct{}, len(ld.Data)+len(c.injectLabels))
	for _, d := range ld.Data {
		seen[d] = struct{}{}
	}
	switch {
	case strings.HasSuffix(path, "/"+mnLabels):
		for k := range c.injectLabels {
			if _, ok := seen[k]; !ok {
				ld.Data = append(ld.Data, k)
				seen[k] = struct{}{}
			}
		}
	case strings.HasSuffix(path, "/values"):
		if i := strings.LastIndex(path, "/"+mnLabel+"/"); i >= 0 {
			name := strings.TrimSuffix(path[i+len("/"+mnLabel+"/"):], "/values")
			if v, ok := c.injectLabels[name]; ok {
				if _, has := seen[v]; !has {
					ld.Data = append(ld.Data, v)
				}
			}
		}
	default:
		return body
	}
	out, err := json.Marshal(&ld)
	if err != nil {
		return body
	}
	return out
}

func defaultWrite(statusCode int, w http.ResponseWriter, b []byte) {
	// We don't know the post-decompression length of b at the header layer above
	// us; let net/http compute it from the Write call.
	w.Header().Del(headers.NameContentLength)
	w.WriteHeader(statusCode)
	w.Write(b)
}
