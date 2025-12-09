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
	"fmt"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
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
	m := make(sets.Set[WFSeriesData], len(s.Data))
	m.SetAll(s.Data)
	for _, s2 := range results {
		s.Envelope.Merge(s2.Envelope)
		for _, d := range s2.Data {
			if !m.Contains(d) {
				m.Set(d)
				s.Data = append(s.Data, d)
			}
		}
	}
}

// MergeAndWriteSeries merges the provided Responses into a single prometheus Series data object,
// and writes it to the provided ResponseWriter
func MergeAndWriteSeries(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {
	s, responses, bestResp := unmarshalAndMerge(r, rgs, "series", func() *WFSeries {
		return &WFSeries{}
	})

	if !handleMergeResult(w, r, s, responses, bestResp) {
		return
	}

	var sep string
	if len(s.Data) > 0 {
		w.Write([]byte(`,"data":[`))
		for _, series := range s.Data {
			fmt.Fprintf(w, `%s{"__name__":"%s","instance":"%s","job":"%s"}`,
				sep, series.Name, series.Instance, series.Job)
			sep = ","
		}
		w.Write([]byte("]"))
	}
	w.Write([]byte("}")) // complete the envelope
}
