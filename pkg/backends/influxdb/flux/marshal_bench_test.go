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

package flux

import (
	"fmt"
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func BenchmarkMarshalCSV(b *testing.B) {
	header := testDataSet().Results[0].SeriesList[0].Header
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10, 60}} {
		ds := testDataSet()
		ds.Results[0].SeriesList = nil
		for i := range shape.series {
			s := dataset.NewSeries(header.Clone(), nil)
			s.Header.Tags["hostname"] = fmt.Sprintf("host-%d", i)
			pts := make(dataset.Points, shape.points)
			for j := range pts {
				pts[j] = dataset.Point{Epoch: epoch.Epoch(int64(1577836800+60*j) * 1e9),
					Values: []any{float64(i*j%9973) / 7, float64(j) + 0.5}}
			}
			s.SetPoints(pts)
			ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, s)
		}
		frb := DefaultJSONRequestBody()
		b.Run(fmt.Sprintf("%dx%d", shape.series, shape.points), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := marshalTimeseriesCSVWriter(ds, frb, 200, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
