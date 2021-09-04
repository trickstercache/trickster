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
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

// WFLabelData is the Wire Format Document for the /labels and /label/<name>/values endpoints
// and will work for any other response where Data is a string slice
type WFLabelData struct {
	*Envelope
	Data []string `json:"data"`
}

// Merge merges the passed WFSeries into the subject WFSeries
func (ld *WFLabelData) Merge(results ...*WFLabelData) {
	m := map[string]interface{}{}
	for _, d := range ld.Data {
		m[d] = nil
	}
	for _, ld2 := range results {
		ld.Envelope.Merge(ld2.Envelope)
		for _, d := range ld2.Data {
			if _, ok := m[d]; !ok {
				m[d] = nil
				ld.Data = append(ld.Data, d)
			}
		}
	}
}

// MergeAndWriteLabelData merges the provided Responses into a single prometheus basic data object,
// and writes it to the provided ResponseWriter
func MergeAndWriteLabelData(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {
	var ld *WFLabelData

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
				ld1 := &WFLabelData{}
				err := json.Unmarshal(rg.Body(), &ld1)
				if err != nil {
					logging.Error(rg.Resources.Logger, "labels unmarshaling error",
						logging.Pairs{"provider": "prometheus", "detail": err.Error()})
					continue
				}
				if ld == nil {
					ld = ld1
				} else {
					ld.Merge(ld1)
				}
			}
			if bestResp == nil || resp.StatusCode < bestResp.StatusCode {
				bestResp = resp
				resp.Body = io.NopCloser(bytes.NewReader(rg.Body()))
			}
		}
	}

	statusCode := 0
	if ld == nil || len(responses) == 0 {
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
	ld.StartMarshal(w, statusCode)

	if len(ld.Data) > 0 {
		sort.Strings(ld.Data)
		w.Write([]byte(fmt.Sprintf(`,"data":["%s"]`, strings.Join(ld.Data, `","`))))
	}
	w.Write([]byte("}"))
}
