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
	"math"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func largeDataSet(n int) *dataset.DataSet {
	pts := make(dataset.Points, n)
	base := int64(1_787_000_000)
	for i := range pts {
		e := epoch.FromSecs(base + int64(i)*10)
		if i%10 == 9 {
			pts[i] = newPoint(e, nil)
		} else {
			v := math.Sin(float64(i) / 100)
			pts[i] = newPoint(e, &v)
		}
	}
	trq := &timeseries.TimeRangeQuery{Statement: "bench.series", Step: 10 * time.Second}
	s, err := newSeries("bench.series", nil, pts, trq)
	if err != nil {
		panic(err)
	}
	return &dataset.DataSet{TimeRangeQuery: trq,
		Results: []*dataset.Result{{SeriesList: []*dataset.Series{s}}}}
}

func BenchmarkMarshalLargeSeries(b *testing.B) {
	for _, n := range []int{50_000, 500_000} {
		ds := largeDataSet(n)
		cases := []struct {
			name string
			rlo  *timeseries.RequestOptions
		}{
			{"json", &timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: FormatJSON}}},
			{"json_mdp1000", &timeseries.RequestOptions{
				ProviderRequest: RenderOptions{Format: FormatJSON, MaxDataPoints: 1000}}},
			{"raw", &timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: FormatRaw}}},
			{"msgpack", &timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: FormatMsgPack}}},
		}
		for _, tc := range cases {
			b.Run(fmt.Sprintf("%s_%d", tc.name, n), func(b *testing.B) {
				b.ReportAllocs()
				var bytesOut int
				for b.Loop() {
					out, err := MarshalTimeseries(ds, tc.rlo, 200)
					if err != nil {
						b.Fatal(err)
					}
					bytesOut = len(out)
				}
				b.ReportMetric(float64(bytesOut), "respbytes")
			})
		}
	}
}
