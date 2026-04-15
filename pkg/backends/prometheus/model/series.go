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
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// WFSeries is the Wire Format Document for the /series endpoint
type WFSeries struct {
	*Envelope
	Data []WFSeriesData `json:"data"`
}

// WFSeriesData is a single label set as returned by Prometheus /api/v1/series.
// Prometheus series carry an arbitrary label set (e.g. shard, region,
// cluster), so a fixed struct would silently drop data on round-trip and
// collapse otherwise-distinct series during merge.
type WFSeriesData map[string]string

func (d WFSeriesData) canonicalKey() string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(d[k])
		sb.WriteByte('\x00')
	}
	return sb.String()
}

// Merge merges the passed WFSeries into the subject WFSeries
func (s *WFSeries) Merge(results ...*WFSeries) {
	m := make(sets.Set[string], len(s.Data))
	for _, d := range s.Data {
		m.Set(d.canonicalKey())
	}
	for _, s2 := range results {
		s.Envelope.Merge(s2.Envelope)
		for _, d := range s2.Data {
			k := d.canonicalKey()
			if !m.Contains(k) {
				m.Set(k)
				s.Data = append(s.Data, d)
			}
		}
	}
}

// MergeAndWriteSeriesMergeFunc returns a MergeFunc for WFSeries
func MergeAndWriteSeriesMergeFunc() merge.MergeFunc {
	return MakeMergeFunc("series", func() *WFSeries {
		return &WFSeries{}
	})
}

// MergeAndWriteSeriesRespondFunc returns a RespondFunc for WFSeries
func MergeAndWriteSeriesRespondFunc() merge.RespondFunc {
	return MakeRespondFunc(func(w http.ResponseWriter, r *http.Request, s *WFSeries, statusCode int) {
		if s == nil {
			return
		}
		s.StartMarshal(w, statusCode)
		w.Write([]byte(`,"data":[`))
		var sep string
		for _, series := range s.Data {
			b, err := json.Marshal(map[string]string(series))
			if err != nil {
				continue
			}
			w.Write([]byte(sep))
			w.Write(b)
			sep = ","
		}
		w.Write([]byte("]}"))
	})
}
