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

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func BenchmarkMarshalJSON(b *testing.B) {
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "UInt64", OutputPosition: 0},
		{Name: "hostname", Role: timeseries.RoleTag, SDataType: "String", OutputPosition: 1},
		{Name: "avg_query", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 2},
		{Name: "count", Role: timeseries.RoleValue, SDataType: "UInt64", OutputPosition: 3},
	}
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10, 60}} {
		series := make([]*dataset.Series, shape.series)
		for i := range series {
			s := dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"hostname": fmt.Sprintf("host-%d", i)}}, nil)
			pts := make(dataset.Points, shape.points)
			for j := range pts {
				pts[j] = dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
					Values: []any{float64(i*j%9973) / 7, int64(j)}}
			}
			s.SetPoints(pts)
			series[i] = s
		}
		ds := jsonTestDataSet(timeseries.DateTimeUnixMilli, fds, series...)
		b.Run(fmt.Sprintf("%dx%d", shape.series, shape.points), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := marshalTimeseriesJSON(io.Discard, ds, nil, 200); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMarshalXSV(b *testing.B) {
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, DataType: timeseries.DateTimeUnixMilli, OutputPosition: 0},
		{Name: "hostname", Role: timeseries.RoleTag, SDataType: "String", OutputPosition: 1},
		{Name: "avg_query", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 2},
		{Name: "count", Role: timeseries.RoleValue, SDataType: "UInt64", OutputPosition: 3},
	}
	series := make([]*dataset.Series, 100)
	for i := range series {
		s := dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"hostname": fmt.Sprintf("host-%d", i)}}, nil)
		pts := make(dataset.Points, 1000)
		for j := range pts {
			pts[j] = dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
				Values: []any{float64(i*j%9973) / 7, int64(j)}}
		}
		s.SetPoints(pts)
		series[i] = s
	}
	ds := jsonTestDataSet(timeseries.DateTimeUnixMilli, fds, series...)
	for _, format := range []struct {
		name string
		code byte
	}{{"csv", 1}, {"tsv_names_types", 5}} {
		rlo := &timeseries.RequestOptions{OutputFormat: format.code}
		b.Run("100x1000/"+format.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := MarshalTimeseriesWriter(ds, rlo, 200, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMarshalNative(b *testing.B) {
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime64(3)", OutputPosition: 0},
		{Name: "hostname", Role: timeseries.RoleTag, SDataType: "String", OutputPosition: 1},
		{Name: "avg_query", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 2},
		{Name: "count", Role: timeseries.RoleValue, SDataType: "Int64", OutputPosition: 3},
	}
	series := make([]*dataset.Series, 100)
	for i := range series {
		s := dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"hostname": fmt.Sprintf("host-%d", i)}}, nil)
		pts := make(dataset.Points, 1000)
		for j := range pts {
			pts[j] = dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
				Values: []any{float64(i*j%9973) / 7, int64(j)}}
		}
		s.SetPoints(pts)
		series[i] = s
	}
	ds := jsonTestDataSet(timeseries.DateTimeUnixMilli, fds, series...)
	rlo := &timeseries.RequestOptions{OutputFormat: OutputFormatNative}
	b.ReportAllocs()
	for b.Loop() {
		if err := MarshalTimeseriesWriter(ds, rlo, 200, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
