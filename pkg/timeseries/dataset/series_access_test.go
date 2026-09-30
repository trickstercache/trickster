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
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
	"github.com/tinylib/msgp/msgp"
)

// a series of every kind, one column each, over two rows
func kindsSeries() *Series {
	return NewSeries(SeriesHeader{Name: "kinds"}, Points{
		{Epoch: 1, Values: []any{nil, true, int64(-2), uint64(3), 1.5, "a\"b", []byte("raw"), json.Number("12"),
			map[string]any{"k": "v"}}},
		{Epoch: 2, Values: []any{nil, false, int64(4), uint64(5), math.NaN(), "", []byte{}, json.Number("1e3"),
			map[string]any{}}},
	})
}

func TestBuilderTypedAdders(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{})
	b.Grow(1, 8, 16)
	b.StartSeries(SeriesHeader{Name: "s"})
	r := b.Row()
	r.SetEpoch(1)
	r.AddNull()
	r.AddBool(true)
	r.AddInt64(-1)
	r.AddUint64(2)
	r.AddFloat64(0.5)
	r.AddString([]byte("text"))
	r.AddBytes([]byte("raw"))
	r.AddNumber([]byte("42"))
	require.NoError(t, r.Commit())
	ds, err := b.Finish()
	require.NoError(t, err)
	require.Equal(t, Points{{Epoch: 1, Values: []any{nil, true, int64(-1), uint64(2), 0.5, "text", []byte("raw"),
		json.Number("42")}}}, ds.Results[0].SeriesList[0].Points())
}

func TestSegmentAndRowAccessors(t *testing.T) {
	s := kindsSeries()
	seg := &s.Segments()[0]
	require.Equal(t, KindBool, seg.KindAt(1, 0))
	require.True(t, seg.Bool(1, 0))
	require.Equal(t, int64(-2), seg.Int64(2, 0))
	require.Equal(t, uint64(3), seg.Uint64(3, 0))
	require.Equal(t, 1.5, seg.Float64(4, 0))
	require.Equal(t, "a\"b", seg.Text(5, 0))
	require.Equal(t, []byte("raw"), seg.Bytes(6, 0))
	r := &Result{SeriesList: SeriesList{s}}
	var rows int
	for row := range r.Rows(RowOrder{}) {
		require.Equal(t, KindInt64, row.KindAt(2))
		require.Equal(t, seg.Int64(2, row.Index), row.Int64(2))
		require.Equal(t, seg.Bytes(6, row.Index), row.Bytes(6))
		rows++
	}
	require.Equal(t, 2, rows)
	require.True(t, s.IsSorted())
	require.Equal(t, describePoints(s.Points()[1:]), describePoints(Points{s.PointAt(1)}))
	require.Panics(t, func() { s.PointAt(2) })
	s.SetSegments(nil)
	require.Zero(t, s.PointCount())
	require.Nil(t, s.Points())
}

func TestSegmentJSONAndFormatting(t *testing.T) {
	seg := &kindsSeries().Segments()[0]
	want := [][]string{
		{"null", "true", "-2", "3", "1.5", `"a\"b"`, `"cmF3"`, "12", `{"k":"v"}`},
		{"null", "false", "4", "5", "", `""`, `""`, "1e3", `{}`},
	}
	for i := range 2 {
		for c := range seg.NumCols() {
			got, err := seg.AppendJSON(nil, c, i)
			check := seg.CheckJSON(c, i)
			if want[i][c] == "" {
				// NaN has no JSON form
				require.Error(t, err)
				require.Error(t, check)
				continue
			}
			require.NoError(t, err)
			require.NoError(t, check)
			require.Equal(t, want[i][c], string(got), "row %d column %d", i, c)
		}
	}
	formatted := []string{"<nil>", "true", "-2", "3", "1.5"}
	for c, f := range formatted {
		got, ok := seg.AppendFormatted(nil, c, 0)
		require.True(t, ok)
		require.Equal(t, f, string(got))
	}
	for c := len(formatted); c < seg.NumCols(); c++ {
		_, ok := seg.AppendFormatted(nil, c, 0)
		require.False(t, ok)
	}
	require.Equal(t, `a"b`, seg.FormatText(5, 0))
	require.Equal(t, "[114 97 119]", seg.FormatText(6, 0))
	require.Equal(t, "12", seg.FormatText(7, 0))
}

func TestSegmentsFilter(t *testing.T) {
	pts := Points{
		{Epoch: 1, Values: []any{"a"}}, {Epoch: 2, Values: []any{"b"}},
		{Epoch: 3, Values: []any{"c"}}, {Epoch: 4, Values: []any{"d"}},
	}
	segs := segmentsOf(pts, 1, 2)
	all := segs.Filter(func(*Segment, int) bool { return true })
	require.Equal(t, len(segs), len(all), "a filter that keeps every row is the list itself")
	odd := segs.Filter(func(seg *Segment, i int) bool { return seg.Epoch(i)%2 == 1 })
	require.Equal(t, Points{pts[0], pts[2]}, pointsOf(odd))
	late := segs.Filter(func(seg *Segment, i int) bool { return seg.Epoch(i) > 1 })
	require.Equal(t, pts[1:], pointsOf(late))
	require.Nil(t, segs.Filter(func(*Segment, int) bool { return false }))
}

func TestSeriesMsgpStreamsAndRejects(t *testing.T) {
	s := kindsSeries()
	var buf bytes.Buffer
	w := msgp.NewWriter(&buf)
	require.NoError(t, s.EncodeMsg(w))
	require.NoError(t, w.Flush())
	var got Series
	require.NoError(t, got.DecodeMsg(msgp.NewReader(&buf)))
	require.Equal(t, describePoints(s.Points()), describePoints(got.Points()))
	require.Positive(t, s.Msgsize())
	require.Error(t, got.DecodeMsg(msgp.NewReader(&bytes.Buffer{})))

	// a series in the layout from before columns is refetched rather than read as empty
	legacy := msgp.AppendMapHeader(nil, 1)
	legacy = msgp.AppendString(legacy, seriesKeyLegacyPoints)
	legacy = msgp.AppendArrayHeader(legacy, 0)
	_, err := got.UnmarshalMsg(legacy)
	require.ErrorIs(t, err, ErrLegacySeries)

	// an unknown key is skipped, and corrupt rows fail
	unknown := msgp.AppendMapHeader(nil, 1)
	unknown = msgp.AppendString(unknown, "extra")
	unknown = msgp.AppendInt(unknown, 7)
	_, err = got.UnmarshalMsg(unknown)
	require.NoError(t, err)
	corrupt := msgp.AppendMapHeader(nil, 1)
	corrupt = msgp.AppendString(corrupt, seriesKeySegments)
	corrupt = msgp.AppendBytes(corrupt, []byte{9})
	_, err = got.UnmarshalMsg(corrupt)
	require.ErrorIs(t, err, ErrInvalidSegments)
	for _, bad := range [][]byte{nil, msgp.AppendMapHeader(nil, 1), append(msgp.AppendMapHeader(nil, 1), 0xc1)} {
		_, err = got.UnmarshalMsg(bad)
		require.Error(t, err)
	}
	unencodable := NewSeries(SeriesHeader{}, Points{{Epoch: 1, Values: []any{make(chan int)}}})
	_, err = unencodable.MarshalMsg(nil)
	require.Error(t, err)
}

func TestDataSetCodecRoundTrip(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Step: 60e9, Statement: "q"}
	ds := &DataSet{TimeRangeQuery: trq, Results: Results{{SeriesList: SeriesList{kindsSeries()}}}}
	b, err := MarshalDataSet(ds, nil, 0)
	require.NoError(t, err)
	for _, shift := range []int{0, 3} {
		// a blob at an unaligned address is copied to an aligned one first
		in := make([]byte, len(b)+shift)[shift:]
		copy(in, b)
		ts, err := UnmarshalDataSet(in, nil)
		require.NoError(t, err)
		got := ts.(*DataSet)
		require.Equal(t, trq.Step, got.TimeRangeQuery.Step)
		require.Equal(t, describePoints(ds.Results[0].SeriesList[0].Points()), describePoints(got.Results[0].SeriesList[0].Points()))
	}
	appended, err := AppendDataSet([]byte("prefix"), ds)
	require.NoError(t, err)
	got, err := ReadDataSet(appended[len("prefix"):], trq)
	require.NoError(t, err)
	require.Equal(t, 2, got.PointCount())
	_, err = UnmarshalDataSet([]byte{0xc1}, nil)
	require.Error(t, err)
	_, err = MarshalDataSet(nil, nil, 0)
	require.True(t, errors.Is(err, timeseries.ErrUnknownFormat))
	// a dataset without a query takes the one it's read with
	b, _ = MarshalDataSet(&DataSet{}, nil, 0)
	got, err = ReadDataSet(b, trq)
	require.NoError(t, err)
	require.Same(t, trq, got.TimeRangeQuery)
}

func TestMergePartsHonorsMerger(t *testing.T) {
	var called bool
	ds := &DataSet{Merger: func(bool, ...timeseries.Timeseries) { called = true }}
	ds.MergeParts(true, &DataSet{})
	require.True(t, called)
	require.False(t, (*DataSet)(nil).HasParts())
	withNil := &DataSet{Results: Results{nil, {SeriesList: SeriesList{nil, NewSeries(SeriesHeader{}, nil)}}}}
	require.False(t, withNil.HasParts())
	require.Same(t, withNil, withNil.Flat())
}

func TestSegmentCheckColumnJSON(t *testing.T) {
	seg := &kindsSeries().Segments()[0]
	for c := range seg.NumCols() {
		// only the float column holds a value, NaN, that JSON can't
		if err := seg.CheckColumnJSON(c); c == 4 {
			require.Error(t, err)
		} else {
			require.NoError(t, err, "column %d", c)
		}
	}
	// a view checks only its own rows
	view := kindsSeries().Segments().View(1, 1)
	require.NoError(t, view[0].CheckColumnJSON(4))
	mixed := NewSeries(SeriesHeader{}, Points{
		{Epoch: 1, Values: []any{1.5}}, {Epoch: 2, Values: []any{"x"}}, {Epoch: 3, Values: []any{math.Inf(1)}},
	}).Segments()
	require.Error(t, mixed[0].CheckColumnJSON(0))
	require.NoError(t, mixed.View(1, 2)[0].CheckColumnJSON(0))
	floats := NewSeries(SeriesHeader{}, Points{{Epoch: 1, Values: []any{1.5}}, {Epoch: 2, Values: []any{math.Inf(-1)}}})
	require.Error(t, floats.Segments()[0].CheckColumnJSON(0))
}

func TestSeriesMsgsizeBoundsEncoding(t *testing.T) {
	// the estimate sizes a marshal's buffer, so an encoding must never outgrow it
	rng := weaktest.NewRand(21, 22)
	for iter := range 500 {
		cols := 1 + rng.IntN(12)
		pts := randPoints(rng, rng.IntN(200), cols, 400, true, randProfiles(rng, cols))
		s := NewSeriesOf(SeriesHeader{Name: "m", Tags: Tags{"host": "a"}}, segmentsOf(pts, cols, rng.IntN(len(pts)+1)))
		if iter%3 == 0 {
			s.SetSegments(s.Segments().View(epoch.Epoch(rng.IntN(2000)), epoch.Epoch(2000+rng.IntN(2000))))
		}
		b, err := s.MarshalMsg(nil)
		require.NoError(t, err)
		require.LessOrEqual(t, len(b), s.Msgsize(), "iteration %d", iter)
	}
}
