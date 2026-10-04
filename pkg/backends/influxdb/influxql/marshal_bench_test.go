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

package influxql

import (
	"fmt"
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// series x points, each point a float and an integer field, a minute apart
func benchDataSet(series, points int) *dataset.DataSet {
	r := &dataset.Result{}
	for i := range series {
		s := marshalTestSeries("cpu", dataset.Tags{"host": fmt.Sprintf("host-%d", i), "region": "us-east-1"}, 0,
			[]string{"usage_user", "count"})
		pts := make(dataset.Points, points)
		for j := range pts {
			pts[j] = dataset.Point{
				Epoch:  epoch.Epoch(int64(1700000000+60*j) * 1e9),
				Values: []any{float64(i*j%9973) / 7, float64(j)},
			}
		}
		s.SetPoints(pts)
		r.SeriesList = append(r.SeriesList, s)
	}
	return &dataset.DataSet{Results: []*dataset.Result{r}}
}

func BenchmarkMarshalTimeseriesWriter(b *testing.B) {
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10, 60}} {
		ds := benchDataSet(shape.series, shape.points)
		for _, tf := range []struct {
			name string
			code byte
		}{{"rfc3339", 0}, {"epoch_ms", 3}} {
			rlo := &timeseries.RequestOptions{TimeFormat: tf.code}
			b.Run(fmt.Sprintf("%dx%d/%s", shape.series, shape.points, tf.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := MarshalTimeseriesWriter(ds, rlo, 200, io.Discard); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
