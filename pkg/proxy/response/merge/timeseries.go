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

package merge

import (
	"bytes"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Timeseries merges the provided Responses into a single Timeseries DataSet
// and writes it to the provided responsewriter
func Timeseries(w http.ResponseWriter, r *http.Request, rgs ResponseGates) {

	var f timeseries.MarshalWriterFunc
	var rlo *timeseries.RequestOptions

	responses := make([]int, len(rgs))
	var bestResp *http.Response

	h := w.Header()
	tsm := make(timeseries.List, len(rgs))
	var k int
	for i, rg := range rgs {

		if rg == nil || rg.Resources == nil ||
			rg.Resources.Response == nil {
			continue
		}

		resp := rg.Resources.Response
		responses[i] = resp.StatusCode

		if rg.Resources.TS != nil {
			headers.Merge(h, rg.Header())
			if f == nil && rg.Resources.TSMarshaler != nil {
				f = rg.Resources.TSMarshaler
			}
			if rlo == nil {
				rlo = rg.Resources.TSReqestOptions
			}
			tsm[k] = rg.Resources.TS
			k++
		}
		if bestResp == nil || resp.StatusCode < bestResp.StatusCode {
			bestResp = resp
			resp.Body = io.NopCloser(bytes.NewReader(rg.Body()))
		}
	}

	if k == 0 || f == nil {
		if bestResp != nil {
			h := w.Header()
			headers.Merge(h, bestResp.Header)
			w.WriteHeader(bestResp.StatusCode)
			io.Copy(w, bestResp.Body)
		} else {
			failures.HandleBadGateway(w, r)
		}
		return
	}

	statusCode := 200
	if bestResp != nil {
		statusCode = bestResp.StatusCode
	}

	headers.StripMergeHeaders(h)
	f(tsm.Merge(false), rlo, statusCode, w)
}
