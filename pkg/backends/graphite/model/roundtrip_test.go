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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestRoundTrip(t *testing.T) {
	// wire -> DataSet -> cache -> DataSet -> wire must be byte-equal for every
	// client format, from both upstream formats
	m := NewModeler()
	for name, body := range map[string]string{"json": sampleJSON, "raw": sampleRaw, "nulls": sampleNulls} {
		t.Run(name, func(t *testing.T) {
			q := trq(0)
			ts, err := m.WireUnmarshaler([]byte(body), q)
			if err != nil {
				t.Fatal(err)
			}
			cached, err := m.CacheMarshaler(ts, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ts2, err := m.CacheUnmarshaler(cached, q)
			if err != nil {
				t.Fatal(err)
			}
			ds2 := ts2.(*dataset.DataSet)
			ds2.TimeRangeQuery = q
			for _, format := range []string{FormatJSON, FormatRaw, FormatCSV, FormatMsgPack} {
				ro := &timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: format, MaxDataPoints: 3}}
				direct, err := m.WireMarshaler(ts, ro, 200)
				if err != nil {
					t.Fatal(err)
				}
				viaCache, err := m.WireMarshaler(ts2, ro, 200)
				if err != nil {
					t.Fatal(err)
				}
				if string(direct) != string(viaCache) {
					t.Errorf("%s differs through the cache:\n%q\n%q", format, direct, viaCache)
				}
			}
			// the cached DataSet still carries nulls as nulls
			orig := ts.(*dataset.DataSet).Results[0].SeriesList[0]
			for _, s := range ds2.Results[0].SeriesList {
				if s.Header.Name != orig.Header.Name {
					continue
				}
				for i, p := range s.Points {
					if p.Values[0] != orig.Points[i].Values[0] {
						t.Errorf("point %d changed through the cache: %v vs %v", i, p.Values[0], orig.Points[i].Values[0])
					}
				}
			}
			// the raw form of the json sample is the raw sample, and vice versa
			if name == "nulls" {
				if raw, _ := m.WireMarshaler(ts, &timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: FormatRaw}}, 200); string(raw) != "dev.fast.cpu.host01.percent,1786996410,1786996440,10|None,None,1.5\n" {
					t.Errorf("json->raw: %q", raw)
				}
			}
			if name == "raw" {
				if js, _ := m.WireMarshaler(ts, nil, 200); string(js) != sampleJSON {
					t.Errorf("raw->json: %s", js)
				}
			}
			// merging a cropped clone back in is lossless
			ds := ts.(*dataset.DataSet)
			clone := ds.CroppedClone(timeseries.Extent{Start: time.Unix(1787349980, 0), End: time.Unix(1787350000, 0)})
			merged := ds.Clone().(*dataset.DataSet)
			merged.Merge(true, clone)
			// Merge orders series by name; order the original the same way
			sorted := ds.Clone().(*dataset.DataSet)
			slices.SortFunc(sorted.Results[0].SeriesList, func(a, b *dataset.Series) int {
				return strings.Compare(a.Header.Name, b.Header.Name)
			})
			a, _ := m.WireMarshaler(sorted, nil, 200)
			b, _ := m.WireMarshaler(merged, nil, 200)
			if string(a) != string(b) {
				t.Errorf("merge not idempotent:\n%s\n%s", a, b)
			}
		})
	}
}
