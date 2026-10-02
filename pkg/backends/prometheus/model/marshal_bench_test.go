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
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type countingWriter struct {
	writes, bytes int
}

func (w *countingWriter) Write(b []byte) (int, error) {
	w.writes++
	w.bytes += len(b)
	return len(b), nil
}

func benchMatrix(series, points int) *dataset.DataSet {
	sl := make(dataset.SeriesList, series)
	for i := range sl {
		pts := make(dataset.Points, points)
		for j := range pts {
			pts[j] = dataset.Point{
				Epoch:  epoch.Epoch(1700000000+j*15) * epoch.Epoch(1e9),
				Values: []any{strconv.FormatFloat(float64(i*j)/7, 'f', -1, 64)},
			}
		}
		sl[i] = dataset.NewSeries(dataset.SeriesHeader{
			Tags: dataset.Tags{
				"__name__": "node_cpu_seconds_total", "job": "node",
				"mode": "idle", "instance": "host-" + strconv.Itoa(i) + ":9100",
			},
			ValueFieldsList: []timeseries.FieldDefinition{{Name: "value"}},
		}, pts)
	}
	return &dataset.DataSet{Results: dataset.Results{{SeriesList: sl}}}
}

func BenchmarkMarshalTSOrVectorWriter(b *testing.B) {
	for _, c := range []struct {
		name           string
		series, points int
		vector         bool
	}{
		{"matrix-100x1000", 100, 1000, false},
		{"matrix-10x60", 10, 60, false},
		{"vector-1000", 1000, 1, true},
	} {
		ds := benchMatrix(c.series, c.points)
		b.Run(c.name, func(b *testing.B) {
			var w countingWriter
			b.ReportAllocs()
			for b.Loop() {
				if err := MarshalTSOrVectorWriter(ds, nil, 200, &w, c.vector); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(w.writes)/float64(b.N), "writes/op")
			b.SetBytes(int64(w.bytes / b.N))
		})
	}
}
