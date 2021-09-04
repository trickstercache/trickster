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

package model

import (
	"bytes"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// MergeAndWriteVector merges the provided Responses into a single prometheus Vector data object,
// and writes it to the provided ResponseWriter
func MergeAndWriteVector(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {

	var ts *dataset.DataSet
	var trq *timeseries.TimeRangeQuery

	responses := make([]int, len(rgs))
	var bestResp *http.Response

	statusCode := 0

	for i, rg := range rgs {
		if rg == nil {
			continue
		}
		if rg.Resources != nil && rg.Resources.Response != nil {
			resp := rg.Resources.Response
			responses[i] = resp.StatusCode

			if resp.Body != nil {
				defer resp.Body.Close()
			}

			if statusCode == 0 || resp.StatusCode < statusCode {
				statusCode = resp.StatusCode
			}

			if resp.StatusCode < 400 {

				if rg.Resources.TimeRangeQuery != nil {
					trq = rg.Resources.TimeRangeQuery
				}

				t2, err := UnmarshalTimeseries(rg.Body(), trq)
				if err != nil {
					logging.Error(rg.Resources.Logger, "vector unmarshaling error",
						logging.Pairs{"provider": "prometheus", "detail": err.Error()})
					continue
				}

				if ts == nil {
					ds, ok := t2.(*dataset.DataSet)
					if !ok {
						logging.Error(rg.Resources.Logger, "vector unmarshaling error",
							logging.Pairs{"provider": "prometheus", "detail": err.Error()})
						continue
					}
					ts = ds
				} else {
					ts.Merge(false, t2)
				}
			}
			if bestResp == nil || resp.StatusCode < bestResp.StatusCode {
				bestResp = resp
				resp.Body = io.NopCloser(bytes.NewReader(rg.Body()))
			}
		}
	}

	if len(responses) == 0 {
		handlers.HandleBadGateway(w, r)
		return
	}

	if ts == nil {
		if bestResp != nil {
			h := w.Header()
			headers.Merge(h, bestResp.Header)
			w.WriteHeader(bestResp.StatusCode)
			io.Copy(w, bestResp.Body)

		} else {
			handlers.HandleBadGateway(w, r)
		}
		return
	}

	MarshalTSOrVectorWriter(ts, nil, statusCode, w, true)

}
