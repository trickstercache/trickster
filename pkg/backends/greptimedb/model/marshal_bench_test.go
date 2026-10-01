/*
 * Copyright 2026 The Trickster Authors
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
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func BenchmarkMarshalTimeseriesWriter(b *testing.B) {
	fields := timeseries.FieldDefinitions{
		{Name: "ts", Role: timeseries.RoleTimestamp, SDataType: "TimestampMillisecond"},
		{Name: "host", Role: timeseries.RoleTag, SDataType: "String"},
		{Name: "usage", Role: timeseries.RoleValue, SDataType: "Float64"},
		{Name: "count", Role: timeseries.RoleValue, SDataType: "Int64"},
	}
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10, 60}} {
		r := &dataset.Result{}
		for i := range shape.series {
			s := dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"host": fmt.Sprintf(`"host-%d"`, i)}}, nil)
			pts := make(dataset.Points, shape.points)
			for j := range pts {
				pts[j] = dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
					Values: []any{float64(i*j%9973) / 7, int64(j)}}
			}
			s.SetPoints(pts)
			r.SeriesList = append(r.SeriesList, s)
		}
		d := &dataSet{DataSet: &dataset.DataSet{Results: dataset.Results{r}}, fields: fields}
		b.Run(fmt.Sprintf("%dx%d", shape.series, shape.points), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := MarshalTimeseriesWriter(d, nil, 200, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
