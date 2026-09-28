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

package streamtest

import (
	"math"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/stretchr/testify/require"
)

func compareBase() *dataset.DataSet {
	fd := timeseries.FieldDefinition{Name: "v", DataType: timeseries.Float64}
	series := func(host string, values ...any) *dataset.Series {
		pts := make(dataset.Points, len(values))
		for i, v := range values {
			pts[i] = dataset.Point{Epoch: 1, Size: 10, Values: []any{v}}
		}
		return &dataset.Series{
			Header: dataset.SeriesHeader{Name: "s", Tags: dataset.Tags{"host": host},
				ValueFieldsList: timeseries.FieldDefinitions{fd}, Size: 5},
			Points:    pts,
			PointSize: 10,
		}
	}
	return &dataset.DataSet{
		Status:     "success",
		Warnings:   []string{"w"},
		ExtentList: timeseries.ExtentList{{Start: time.Unix(1, 0), End: time.Unix(2, 0)}},
		Results: dataset.Results{{
			StatementID: 1,
			SeriesList: dataset.SeriesList{
				series("a", math.NaN(), float32(math.NaN()), []byte("b"), map[string]int{"k": 1}),
				series("b", 1.0),
				nil,
			},
		}, nil},
	}
}

func TestCompare(t *testing.T) {
	require.NoError(t, Compare(nil, nil, CompareOptions{}))
	require.Error(t, Compare(compareBase(), nil, CompareOptions{}))
	require.NoError(t, Compare(compareBase(), compareBase(), CompareOptions{}))

	tests := []struct {
		name   string
		mutate func(*dataset.DataSet)
		want   string
		opts   CompareOptions
	}{
		{"status", func(ds *dataset.DataSet) { ds.Status = "error" }, "status:", CompareOptions{}},
		{"source", func(ds *dataset.DataSet) { ds.SourceResultType = "matrix" }, "sourceResultType:", CompareOptions{}},
		{"warnings", func(ds *dataset.DataSet) { ds.Warnings = nil }, "warnings:", CompareOptions{}},
		{"extent count", func(ds *dataset.DataSet) { ds.ExtentList = nil }, "extents:", CompareOptions{}},
		{"extent", func(ds *dataset.DataSet) { ds.ExtentList[0].End = time.Unix(3, 0) }, "extents:", CompareOptions{}},
		{"volatile", func(ds *dataset.DataSet) { ds.VolatileExtentList = ds.ExtentList }, "volatileExtents:", CompareOptions{}},
		{"results", func(ds *dataset.DataSet) { ds.Results = ds.Results[:1] }, "results: want 2, got 1", CompareOptions{}},
		{"nil result", func(ds *dataset.DataSet) { ds.Results[0] = nil }, "results[0].result: want nil false", CompareOptions{}},
		{"statement", func(ds *dataset.DataSet) { ds.Results[0].StatementID = 2 }, "results[0].result: want {1", CompareOptions{}},
		{"series count", func(ds *dataset.DataSet) { ds.Results[0].SeriesList = ds.Results[0].SeriesList[:1] },
			"results[0].series: want 3, got 1", CompareOptions{}},
		{"nil series", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1] = nil }, "series[1]: want nil false", CompareOptions{}},
		{"order", func(ds *dataset.DataSet) {
			sl := ds.Results[0].SeriesList
			sl[0], sl[1] = sl[1], sl[0]
		}, "series[0].tags", CompareOptions{}},
		{"name", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Header.Name = "x" }, ".name:", CompareOptions{}},
		{"query", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Header.QueryStatement = "x" }, ".query:", CompareOptions{}},
		{"timestamp", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Header.TimestampField.Name = "t" },
			".timestampField:", CompareOptions{}},
		{"tag fields", func(ds *dataset.DataSet) {
			ds.Results[0].SeriesList[0].Header.TagFieldsList = timeseries.FieldDefinitions{{Name: "host"}}
		}, ".tagFields:", CompareOptions{}},
		{"value fields", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Header.ValueFieldsList = nil },
			".valueFields:", CompareOptions{}},
		{"untracked fields", func(ds *dataset.DataSet) {
			ds.Results[0].SeriesList[0].Header.UntrackedFieldsList = timeseries.FieldDefinitions{{Name: "u"}}
		}, ".untrackedFields:", CompareOptions{}},
		{"header size", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Header.Size = 6 }, ".headerSize:", CompareOptions{}},
		{"point size", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].PointSize = 6 }, ".pointSize:", CompareOptions{}},
		{"points", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points = nil }, "series[1].points: want 1", CompareOptions{}},
		{"epoch", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points[0].Epoch = 2 }, ".points[0].epoch:", CompareOptions{}},
		{"size", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points[0].Size = 2 }, ".points[0].size:", CompareOptions{}},
		{"value count", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points[0].Values = nil },
			".points[0].values:", CompareOptions{}},
		{"float", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points[0].Values[0] = 2.0 },
			".points[0].values[0]: want 1, got 2", CompareOptions{}},
		{"float type", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points[0].Values[0] = int64(1) },
			".points[0].values[0]:", CompareOptions{}},
		{"nan", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Points[0].Values[0] = 1.0 },
			".points[0].values[0]:", CompareOptions{}},
		{"float32", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Points[1].Values[0] = float32(1) },
			".points[1].values[0]:", CompareOptions{}},
		{"bytes", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Points[2].Values[0] = []byte("c") },
			".points[2].values[0]:", CompareOptions{}},
		{"other", func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Points[3].Values[0] = map[string]int{} },
			".points[3].values[0]:", CompareOptions{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := compareBase()
			test.mutate(got)
			err := Compare(compareBase(), got, test.opts)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestCompareOptions(t *testing.T) {
	got := compareBase()
	s := got.Results[0].SeriesList[0]
	s.Header.Size, s.PointSize, s.Points[0].Size = 1, 2, 3
	require.Error(t, Compare(compareBase(), got, CompareOptions{}))
	require.NoError(t, Compare(compareBase(), got, CompareOptions{IgnoreSizes: true}))

	got = compareBase()
	sl := got.Results[0].SeriesList
	sl[0], sl[1], sl[2] = sl[2], sl[0], sl[1]
	require.Error(t, Compare(compareBase(), got, CompareOptions{}))
	require.NoError(t, Compare(compareBase(), got, CompareOptions{IgnoreSeriesOrder: true}))
	// reordering must not mutate either DataSet
	require.Nil(t, got.Results[0].SeriesList[0])
}
