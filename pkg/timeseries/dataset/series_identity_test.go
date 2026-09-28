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

package dataset

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

func TestSeriesHeaderHashIsUnambiguous(t *testing.T) {
	field := func(name string, dt timeseries.FieldDataType) timeseries.FieldDefinitions {
		return timeseries.FieldDefinitions{{Name: name, DataType: dt}}
	}
	// each pair once produced identical hash input by running adjacent strings together
	pairs := map[string][2]SeriesHeader{
		"tag boundary":   {{Tags: Tags{"ab": "c"}}, {Tags: Tags{"a": "bc"}}},
		"name and query": {{Name: "ab", QueryStatement: "c"}, {Name: "a", QueryStatement: "bc"}},
		"tags and values": {
			{Tags: Tags{"x": "y\x01"}},
			{Tags: Tags{"x": "y"}, ValueFieldsList: field("", 1)},
		},
		"values and untracked": {
			{ValueFieldsList: field("a", 1)},
			{UntrackedFieldsList: field("a", 1)},
		},
	}
	for name, pair := range pairs {
		a, b := pair[0], pair[1]
		require.NotEqual(t, a.CalculateHash(), b.CalculateHash(), name)
		require.False(t, sameSeries(&a, &b), name)
	}
	h := SeriesHeader{Name: "n", Tags: Tags{"k": "v"}}
	require.Equal(t, h.CalculateHash(), h.CalculateHashWithQueryStatement(""))
}

func collidingSeries(host string, epochs ...epoch.Epoch) *Series {
	s := &Series{Header: SeriesHeader{Name: "s", Tags: Tags{"host": host}}}
	s.Header.hash = 7 // every series here shares one hash
	for _, e := range epochs {
		s.Points = append(s.Points, Point{Epoch: e, Size: 16, Values: []any{int64(e)}})
	}
	s.PointSize = s.Points.Size()
	return s
}

func epochsByHost(sl SeriesList) map[string][]epoch.Epoch {
	out := make(map[string][]epoch.Epoch, len(sl))
	for _, s := range sl {
		out[s.Header.Tags["host"]] = pointEpochs(s)
	}
	return out
}

func TestSeriesIndex(t *testing.T) {
	idx := newSeriesIndex(0, headerOf)
	a, b, c := collidingSeries("a"), collidingSeries("b"), collidingSeries("c")
	idx.add(7, a)
	idx.add(7, b)
	for _, s := range []*Series{a, b} {
		got, ok := idx.find(7, &collidingSeries(s.Header.Tags["host"]).Header)
		require.True(t, ok)
		require.Same(t, s, got)
	}
	_, ok := idx.find(7, &c.Header)
	require.False(t, ok)
	_, ok = idx.find(8, &a.Header)
	require.False(t, ok)
	idx.reset()
	_, ok = idx.find(7, &a.Header)
	require.False(t, ok)
	require.Empty(t, idx.spill)
}

func TestMergesKeepCollidingSeriesApart(t *testing.T) {
	lists := func() (SeriesList, SeriesList) {
		// the receiver repeats "b" and the incoming list repeats "c"; repeats are dropped
		return SeriesList{collidingSeries("a", 1), collidingSeries("b", 1), collidingSeries("b", 9)},
			SeriesList{collidingSeries("a", 2), collidingSeries("c", 3), collidingSeries("c", 4)}
	}
	want := map[string][]epoch.Epoch{"a": {1, 2}, "b": {1}, "c": {3}}

	sl, sl2 := lists()
	require.Equal(t, want, epochsByHost(sl.Merge(sl2, true)))

	sl, sl2 = lists()
	opts := MergeOpts{SortPoints: true, Strategy: merge.StrategySum}
	require.Equal(t, want, epochsByHost(sl.MergeWithOpts(sl2, opts)))

	sl, sl2 = lists()
	out := sl.mergeCollection([]SeriesList{sl2, {collidingSeries("b", 5), collidingSeries("d", 6)}},
		MergeOpts{SortPoints: true})
	require.Equal(t, map[string][]epoch.Epoch{"a": {1, 2}, "b": {1, 5}, "c": {3}, "d": {6}},
		epochsByHost(out))

	// nil series are skipped, and an empty receiver starts from the first member
	var empty SeriesList
	out = empty.mergeCollection([]SeriesList{{collidingSeries("a", 1), nil}, {nil, collidingSeries("b", 2)}},
		MergeOpts{SortPoints: true})
	require.Equal(t, map[string][]epoch.Epoch{"a": {1}, "b": {2}}, epochsByHost(out))
	out = SeriesList{nil, collidingSeries("a", 1)}.mergeCollection(
		[]SeriesList{{collidingSeries("a", 2)}, {collidingSeries("b", 3)}}, MergeOpts{SortPoints: true})
	require.Equal(t, map[string][]epoch.Epoch{"a": {1, 2}, "b": {3}}, epochsByHost(out))
	require.Empty(t, empty.mergeCollection([]SeriesList{{}, nil}, MergeOpts{}))
}

func TestDataSetMergeKeepsCollidingSeries(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Step: time.Second}
	dataSet := func(series ...*Series) *DataSet {
		return &DataSet{TimeRangeQuery: trq, Results: Results{{SeriesList: series}}}
	}
	ds := dataSet(collidingSeries("a", 1), collidingSeries("b", 1))
	ds.Merge(true, dataSet(collidingSeries("a", 2), collidingSeries("c", 3)),
		dataSet(collidingSeries("b", 4)))
	require.Equal(t, map[string][]epoch.Epoch{"a": {1, 2}, "b": {1, 4}, "c": {3}},
		epochsByHost(ds.Results[0].SeriesList))
}

func TestEqualHeaderDetectsCollision(t *testing.T) {
	a := SeriesList{collidingSeries("a")}
	require.True(t, a.EqualHeader(SeriesList{collidingSeries("a")}))
	require.False(t, a.EqualHeader(SeriesList{collidingSeries("b")}))
}
