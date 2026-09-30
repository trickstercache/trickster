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
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// series x points, each point a dimension and two metrics, as a groupBy by one dimension decodes
func benchDruidDataSet(series, points int) *dataset.DataSet {
	r := &dataset.Result{}
	for i := range series {
		page := fmt.Sprintf("page-%d", i)
		s := &dataset.Series{Header: dataset.SeriesHeader{
			Tags:            dataset.Tags{"page": page},
			TagFieldsList:   timeseries.FieldDefinitions{{Name: "page"}},
			ValueFieldsList: timeseries.FieldDefinitions{{Name: "page", ProviderData1: fieldNativeDimension}, {Name: "count"}, {Name: "added"}},
		}}
		s.Points = make(dataset.Points, points)
		for j := range s.Points {
			s.Points[j] = dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
				Values: []any{page, int64(i * j % 97), float64(i*j%9973) / 7}}
		}
		r.SeriesList = append(r.SeriesList, s)
	}
	return &dataset.DataSet{Results: dataset.Results{r}}
}

func BenchmarkMarshalTimeseriesWriter(b *testing.B) {
	sqlPlan := &SQLQueryPlan{Plan: &sqlanalyzer.QueryPlan{OutputColumn: "__time",
		GroupColumns: []string{"page"}, ValueColumns: []string{"count", "added"}}}
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10, 60}} {
		ds := benchDruidDataSet(shape.series, shape.points)
		for _, query := range []struct {
			name string
			plan any
		}{
			{"timeseries", &QueryPlan{queryType: queryTimeseries}},
			{"groupby", &QueryPlan{queryType: queryGroupBy}},
			{"sql", sqlPlan},
		} {
			rlo := &timeseries.RequestOptions{ProviderRequest: query.plan}
			b.Run(fmt.Sprintf("%dx%d/%s", shape.series, shape.points, query.name), func(b *testing.B) {
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
