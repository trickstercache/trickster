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
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
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

	responses, bestResp := gatherResponses(r, rgs, func(rg *merge.ResponseGate) bool {
		if rg.Resources.TimeRangeQuery != nil {
			trq = rg.Resources.TimeRangeQuery
		}

		t2, err := UnmarshalTimeseries(rg.Body(), trq)
		if err != nil {
			logger.Error("vector unmarshaling error",
				logging.Pairs{"provider": "prometheus", "detail": err.Error()})
			return false
		}

		if ts == nil {
			ds, ok := t2.(*dataset.DataSet)
			if !ok {
				logger.Error("vector unmarshaling error",
					logging.Pairs{"provider": "prometheus"})
				return false
			}
			ts = ds
		} else {
			ts.Merge(false, t2)
		}
		return true
	})

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

	MarshalTSOrVectorWriter(ts, nil, bestResp.StatusCode, w, true) //revive:disable:unhandled-error
}
