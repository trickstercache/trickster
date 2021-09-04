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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

// WFSeries is the Wire Format Document for the /series endpoint
type WFSeries struct {
	*Envelope
	Data []WFSeriesData `json:"data"`
}

// WFSeriesData describes the wire format document for series data elements
type WFSeriesData struct {
	Name     string `json:"__name__"`
	Instance string `json:"instance"`
	Job      string `json:"job"`
}

// Merge merges the passed WFSeries into the subject WFSeries
func (s *WFSeries) Merge(results ...*WFSeries) {
	m := map[WFSeriesData]interface{}{}
	for _, d := range s.Data {
		m[d] = nil
	}
	for _, s2 := range results {

		s.Envelope.Merge(s2.Envelope)

		for _, d := range s2.Data {
			if _, ok := m[d]; !ok {
				m[d] = nil
				s.Data = append(s.Data, d)
			}
		}
	}
}

// MergeAndWriteSeries merges the provided Responses into a single prometheus Series data object,
// and writes it to the provided ResponseWriter
func MergeAndWriteSeries(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {
	var s *WFSeries

	responses := make([]int, len(rgs))
	var bestResp *http.Response

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

			if resp.StatusCode < 400 {
				s1 := &WFSeries{}
				err := json.Unmarshal(rg.Body(), &s1)
				if err != nil {
					logging.Error(rg.Resources.Logger, "series unmarshaling error",
						logging.Pairs{"provider": "prometheus", "detail": err.Error()})
					continue
				}
				if s == nil {
					s = s1
				} else {
					s.Merge(s1)
				}
			}
			if bestResp == nil || resp.StatusCode < bestResp.StatusCode {
				bestResp = resp
				resp.Body = io.NopCloser(bytes.NewReader(rg.Body()))
			}
		}
	}

	statusCode := 0
	if s == nil || len(responses) == 0 {
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

	sort.Ints(responses)
	statusCode = responses[0]
	s.StartMarshal(w, statusCode)

	var sep string
	if len(s.Data) > 0 {
		w.Write([]byte(`,"data":[`))
		for _, series := range s.Data {
			w.Write([]byte(fmt.Sprintf(`%s{"__name__":"%s","instance":"%s","job":"%s"}`,
				sep, series.Name, series.Instance, series.Job)))
			sep = ","
		}
		w.Write([]byte("]"))
	}
	w.Write([]byte("}")) // complete the envelope
}
