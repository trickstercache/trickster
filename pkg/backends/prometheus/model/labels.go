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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// WFLabelData is the Wire Format Document for the /labels and /label/<name>/values endpoints
// and will work for any other response where Data is a string slice
type WFLabelData struct {
	*Envelope
	Data []string `json:"data"`
}

// Merge merges the passed WFSeries into the subject WFSeries
func (ld *WFLabelData) Merge(results ...*WFLabelData) {
	m := sets.NewStringSet()
	for _, d := range ld.Data {
		m[d] = struct{}{}
	}
	for _, ld2 := range results {
		ld.Envelope.Merge(ld2.Envelope)
		for _, d := range ld2.Data {
			if _, ok := m[d]; !ok {
				m[d] = struct{}{}
				ld.Data = append(ld.Data, d)
			}
		}
	}
}

// MergeAndWriteLabelData merges the provided Responses into a single prometheus basic data object,
// and writes it to the provided ResponseWriter
func MergeAndWriteLabelData(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {
	var ld *WFLabelData
	responses, bestResp := gatherResponses(r, rgs, func(rg *merge.ResponseGate) bool {
		ld1 := &WFLabelData{}
		err := json.Unmarshal(rg.Body(), &ld1)
		if err != nil {
			logger.Error("labels unmarshaling error",
				logging.Pairs{"provider": "prometheus", "detail": err.Error()})
			return false
		}
		if ld == nil {
			ld = ld1
		} else {
			ld.Merge(ld1)
		}
		return true
	})

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
	statusCode := responses[0]
	ld.StartMarshal(w, statusCode)

	if len(ld.Data) > 0 {
		sort.Strings(ld.Data)
		fmt.Fprintf(w, `,"data":["%s"]`, strings.Join(ld.Data, `","`))
	}
	w.Write([]byte("}"))
}
