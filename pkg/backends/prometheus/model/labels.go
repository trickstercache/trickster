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
	"net/http"
	"sort"

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
	m := getStringSet()
	defer putStringSet(m)
	m.SetAll(ld.Data)

	// Pre-allocate estimated capacity to reduce reallocations
	estimatedSize := len(ld.Data)
	for _, ld2 := range results {
		estimatedSize += len(ld2.Data)
	}
	if cap(ld.Data) < estimatedSize {
		newData := make([]string, len(ld.Data), estimatedSize)
		copy(newData, ld.Data)
		ld.Data = newData
	}

	for _, ld2 := range results {
		ld.Envelope.Merge(ld2.Envelope)
		for _, d := range ld2.Data {
			if _, ok := m[d]; !ok {
				m.Set(d)
				ld.Data = append(ld.Data, d)
			}
		}
	}
}

// MergeAndWriteLabelDataMergeFunc returns a MergeFunc for WFLabelData
func MergeAndWriteLabelDataMergeFunc() merge.MergeFunc {
	return MakeMergeFunc("labels", func() *WFLabelData {
		return &WFLabelData{}
	})
}

// MergeAndWriteLabelDataRespondFunc returns a RespondFunc for WFLabelData
func MergeAndWriteLabelDataRespondFunc() merge.RespondFunc {
	return MakeRespondFunc(func(w http.ResponseWriter, r *http.Request, ld *WFLabelData, statusCode int) {
		if ld == nil {
			return
		}
		ld.StartMarshal(w, statusCode)
		if len(ld.Data) > 0 {
			sort.Strings(ld.Data)
			w.Write([]byte(`,"data":["`))
			for i, label := range ld.Data {
				if i > 0 {
					w.Write([]byte(`","`))
				}
				w.Write([]byte(label))
			}
			w.Write([]byte(`"]`))
		}
		w.Write([]byte("}"))
	})
}
