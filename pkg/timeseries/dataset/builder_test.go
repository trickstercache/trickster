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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/stretchr/testify/require"
)

func testBuilderFields() timeseries.SeriesFields {
	return timeseries.SeriesFields{
		Timestamp: timeseries.FieldDefinition{Name: "t", DataType: timeseries.DateTimeUnixMilli, Role: timeseries.RoleTimestamp},
		Tags: timeseries.FieldDefinitions{
			{Name: "host", DataType: timeseries.String, Role: timeseries.RoleTag, OutputPosition: 1},
			{Name: "dc", DataType: timeseries.String, Role: timeseries.RoleTag, OutputPosition: 2},
		},
		Values: timeseries.FieldDefinitions{
			{Name: "v", DataType: timeseries.Float64, Role: timeseries.RoleValue, OutputPosition: 3},
		},
	}
}

func testBuilderTRQ() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: "select",
		Extent:    timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(100, 0)},
		Step:      time.Second,
	}
}

type testRow struct {
	e    epoch.Epoch
	host string
	dc   string
	v    any
}

func commitRows(t *testing.T, b *Builder, rows ...testRow) {
	t.Helper()
	for _, tr := range rows {
		r := b.Row()
		r.SetEpoch(tr.e)
		r.SetTag(1, []byte(tr.dc))
		r.SetTag(0, []byte(tr.host))
		r.AddValue(tr.v)
		require.NoError(t, r.Commit())
	}
}

func pointEpochs(s *Series) []epoch.Epoch {
	out := make([]epoch.Epoch, len(s.Points))
	for i, p := range s.Points {
		out[i] = p.Epoch
	}
	return out
}

func pointValues(s *Series) []any {
	out := make([]any, len(s.Points))
	for i, p := range s.Points {
		out[i] = p.Values[0]
	}
	return out
}

func requireSizes(t *testing.T, s *Series) {
	t.Helper()
	var total int64
	for _, p := range s.Points {
		require.Equal(t, PointSize(p.Values), p.Size)
		total += int64(p.Size)
	}
	require.Equal(t, total, s.PointSize)
	require.Positive(t, s.Header.Size)
}

func TestBuilderRowMode(t *testing.T) {
	trq := testBuilderTRQ()
	fields := testBuilderFields()
	b := NewBuilder(trq, BuilderOptions{Fields: fields, SeriesName: "sql", QueryStatement: trq.Statement})
	commitRows(t, b,
		testRow{e: 1, host: "a", dc: "x", v: 1.0},
		testRow{e: 1, host: "b", dc: "x", v: 2.0},
		testRow{e: 2, host: "a", dc: "x", v: 3.0},
		testRow{e: 3, host: "a", dc: "x", v: 4.0},
		testRow{e: 2, host: "b", dc: "x", v: 5.0},
	)
	ds, err := b.Finish()
	require.NoError(t, err)
	require.Same(t, trq, ds.TimeRangeQuery)
	require.Equal(t, timeseries.ExtentList{trq.Extent}, ds.ExtentList)
	require.Len(t, ds.Results, 1)
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 2)
	require.Equal(t, Tags{"host": "a", "dc": "x"}, sl[0].Header.Tags)
	require.Equal(t, Tags{"host": "b", "dc": "x"}, sl[1].Header.Tags)
	require.Equal(t, []epoch.Epoch{1, 2, 3}, pointEpochs(sl[0]))
	require.Equal(t, []any{1.0, 3.0, 4.0}, pointValues(sl[0]))
	require.Equal(t, []epoch.Epoch{1, 2}, pointEpochs(sl[1]))
	for _, s := range sl {
		require.Equal(t, "sql", s.Header.Name)
		require.Equal(t, "select", s.Header.QueryStatement)
		require.Equal(t, fields.Timestamp, s.Header.TimestampField)
		require.Equal(t, fields.Tags, s.Header.TagFieldsList)
		require.Equal(t, fields.Values, s.Header.ValueFieldsList)
		requireSizes(t, s)
	}
	// each series owns its field definitions
	sl[0].Header.ValueFieldsList[0].DataType = timeseries.Int64
	require.Equal(t, timeseries.Float64, sl[1].Header.ValueFieldsList[0].DataType)
	require.Equal(t, timeseries.Float64, fields.Values[0].DataType)
}

func TestBuilderSortsOnlyUnorderedSeries(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields()})
	commitRows(t, b,
		testRow{e: 3, host: "a", v: 1.0},
		testRow{e: 1, host: "b", v: 2.0},
		testRow{e: 1, host: "a", v: 3.0},
		testRow{e: 2, host: "b", v: 4.0},
		testRow{e: 2, host: "a", v: 5.0},
	)
	require.True(t, b.results[0].series[0].unordered)
	require.False(t, b.results[0].series[1].unordered)
	ds, err := b.Finish()
	require.NoError(t, err)
	require.Nil(t, ds.ExtentList)
	sl := ds.Results[0].SeriesList
	require.Equal(t, []epoch.Epoch{1, 2, 3}, pointEpochs(sl[0]))
	require.Equal(t, []any{3.0, 5.0, 1.0}, pointValues(sl[0]))
	require.Equal(t, []epoch.Epoch{1, 2}, pointEpochs(sl[1]))
}

func TestBuilderDuplicatePolicies(t *testing.T) {
	ordered := []testRow{
		{e: 1, host: "a", v: "a1"}, {e: 2, host: "a", v: "a2"}, {e: 2, host: "a", v: "a2b"},
		{e: 2, host: "a", v: "a2c"}, {e: 3, host: "a", v: "a3"},
	}
	unordered := []testRow{
		{e: 2, host: "a", v: "a2"}, {e: 1, host: "a", v: "a1"}, {e: 2, host: "a", v: "a2b"},
		{e: 3, host: "a", v: "a3"}, {e: 2, host: "a", v: "a2c"},
	}
	tests := []struct {
		name   string
		policy DuplicatePolicy
		rows   []testRow
		want   []any
	}{
		{"keep ordered", DuplicatesKeep, ordered, []any{"a1", "a2", "a2b", "a2c", "a3"}},
		{"keep unordered", DuplicatesKeep, unordered, []any{"a1", "a2", "a2b", "a2c", "a3"}},
		{"first ordered", DuplicatesFirstWins, ordered, []any{"a1", "a2", "a3"}},
		{"first unordered", DuplicatesFirstWins, unordered, []any{"a1", "a2", "a3"}},
		{"last ordered", DuplicatesLastWins, ordered, []any{"a1", "a2c", "a3"}},
		{"last unordered", DuplicatesLastWins, unordered, []any{"a1", "a2c", "a3"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields(), Duplicates: test.policy})
			commitRows(t, b, test.rows...)
			ds, err := b.Finish()
			require.NoError(t, err)
			s := ds.Results[0].SeriesList[0]
			require.Equal(t, test.want, pointValues(s))
			requireSizes(t, s)
		})
	}
}

func TestBuilderDuplicateError(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields(), Duplicates: DuplicatesError})
	commitRows(t, b, testRow{e: 1, host: "a", v: 1.0})
	r := b.Row()
	r.SetEpoch(1)
	r.SetTag(0, []byte("a"))
	r.SetTag(1, nil)
	r.AddValue(2.0)
	require.ErrorIs(t, r.Commit(), ErrDuplicateEpoch)
	require.ErrorIs(t, ErrDuplicateEpoch, timeseries.ErrInvalidBody)

	b = NewBuilder(nil, BuilderOptions{Fields: testBuilderFields(), Duplicates: DuplicatesError})
	commitRows(t, b,
		testRow{e: 2, host: "a", v: 1.0},
		testRow{e: 1, host: "a", v: 2.0},
		testRow{e: 2, host: "a", v: 3.0},
	)
	_, err := b.Finish()
	require.ErrorIs(t, err, ErrDuplicateEpoch)
}

func TestBuilderFirstWinsReleasesValues(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields(), Duplicates: DuplicatesFirstWins})
	commitRows(t, b, testRow{e: 1, host: "a", v: 1.0})
	used := len(b.arena)
	commitRows(t, b, testRow{e: 1, host: "a", v: 2.0})
	require.Len(t, b.arena, used)
	// values that are not the latest allocation cannot be released
	b.releaseValues([]any{1})
	b.releaseValues(nil)
	require.Len(t, b.arena, used)
}

func TestBuilderTags(t *testing.T) {
	var calls int
	b := NewBuilder(nil, BuilderOptions{
		Fields: testBuilderFields(),
		TagString: func(fd timeseries.FieldDefinition, raw []byte) string {
			calls++
			return fd.Name + "=" + strings.ToUpper(string(raw))
		},
		SortSeries: true,
	})
	add := func(host []byte, setDC bool) {
		r := b.Row()
		r.SetEpoch(1)
		if host != nil {
			r.SetTag(0, host)
		}
		if setDC {
			r.SetTag(1, []byte("x"))
		}
		r.AddValue(1.0)
		require.NoError(t, r.Commit())
	}
	add([]byte("b"), true)
	add([]byte("b"), true)
	add(nil, true)
	add([]byte(""), true)
	add([]byte("a"), false)
	// a tag set twice keeps its last value
	r := b.Row()
	r.SetEpoch(2)
	r.SetTag(0, []byte("zzz"))
	r.SetTag(0, []byte("b"))
	r.SetTag(1, []byte("x"))
	r.AddValue(1.0)
	require.NoError(t, r.Commit())

	ds, err := b.Finish()
	require.NoError(t, err)
	require.Equal(t, 6, calls)
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 4)
	got := make([]Tags, len(sl))
	for i, s := range sl {
		got[i] = s.Header.Tags
	}
	// SortByTags orders series by their tags' JSON encoding
	require.Equal(t, []Tags{
		{"dc": "dc=X", "host": "host="},
		{"dc": "dc=X", "host": "host=B"},
		{"dc": "dc=X"},
		{"host": "host=A"},
	}, got)
	require.Equal(t, []epoch.Epoch{1, 1, 2}, pointEpochs(sl[1]))
}

func TestBuilderInvalidRows(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields()})
	r := b.Row()
	r.SetTag(0, []byte("a"))
	r.AddValue(1.0)
	require.ErrorIs(t, r.Commit(), ErrInvalidRow)

	r = b.Row()
	r.SetEpoch(1)
	r.SetTag(2, []byte("a"))
	r.AddValue(1.0)
	require.ErrorIs(t, r.Commit(), ErrInvalidRow)

	r = b.Row()
	r.SetEpoch(1)
	r.SetTag(-1, []byte("a"))
	r.AddValue(1.0)
	require.ErrorIs(t, r.Commit(), ErrInvalidRow)

	r = b.Row()
	r.SetEpoch(1)
	r.SetTag(0, []byte("a"))
	r.AddValue(1.0)
	r.AddValue(2.0)
	require.ErrorIs(t, r.Commit(), ErrInvalidRow)
	require.ErrorIs(t, ErrInvalidRow, timeseries.ErrInvalidBody)

	ds, err := b.Finish()
	require.NoError(t, err)
	require.Empty(t, ds.Results[0].SeriesList)
}

func TestBuilderSeriesMode(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{})
	h := SeriesHeader{
		Name:            "up",
		Tags:            Tags{"job": "a"},
		ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.String}},
	}
	b.StartSeries(h)
	for _, e := range []epoch.Epoch{2, 1, 3} {
		r := b.Row()
		r.SetEpoch(e)
		r.AddValue("1")
		require.NoError(t, r.Commit())
	}
	require.NoError(t, b.AppendPoint(Point{Epoch: 4, Values: []any{"42"}}))
	require.NoError(t, b.AppendPoint(Point{Epoch: 5, Values: []any{"7"}, Size: 99}))

	r := b.Row()
	r.SetEpoch(6)
	r.AddValue("1")
	r.AddValue("2")
	require.ErrorIs(t, r.Commit(), ErrInvalidRow)
	require.ErrorIs(t, b.AppendPoint(Point{Epoch: 6}), ErrInvalidRow)

	// tags cannot be set on rows committed to an open series
	b2 := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields()})
	b2.StartSeries(SeriesHeader{Name: "x"})
	r2 := b2.Row()
	r2.SetEpoch(1)
	r2.SetTag(0, []byte("a"))
	require.ErrorIs(t, r2.Commit(), ErrInvalidRow)
	// a series without value fields accepts any number of values
	r2 = b2.Row()
	r2.SetEpoch(1)
	require.NoError(t, r2.Commit())

	b.StartSeries(SeriesHeader{Name: "down", Tags: Tags{"job": "b"}})
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"1", "2"}}))
	b.EndSeries()
	require.ErrorIs(t, b.AppendPoint(Point{Epoch: 1}), ErrInvalidRow)

	ds, err := b.Finish()
	require.NoError(t, err)
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 2)
	require.Equal(t, "up", sl[0].Header.Name)
	require.Equal(t, []epoch.Epoch{1, 2, 3, 4, 5}, pointEpochs(sl[0]))
	require.Equal(t, PointSize([]any{"42"}), sl[0].Points[3].Size)
	require.Equal(t, 99, sl[0].Points[4].Size)
	require.Equal(t, "down", sl[1].Header.Name)
}

func TestBuilderSeriesModeDuplicates(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Duplicates: DuplicatesLastWins})
	b.StartSeries(SeriesHeader{Name: "s"})
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"a"}}))
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"bb"}}))
	b.StartSeries(SeriesHeader{Name: "t"})
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"a"}}))
	ds, err := b.Finish()
	require.NoError(t, err)
	s := ds.Results[0].SeriesList[0]
	require.Equal(t, []any{"bb"}, pointValues(s))
	requireSizes(t, s)

	b = NewBuilder(nil, BuilderOptions{Duplicates: DuplicatesFirstWins})
	b.StartSeries(SeriesHeader{Name: "s"})
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"a"}}))
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"bb"}}))
	r := b.Row()
	r.SetEpoch(1)
	require.NoError(t, r.Commit())
	ds, err = b.Finish()
	require.NoError(t, err)
	require.Equal(t, []any{"a"}, pointValues(ds.Results[0].SeriesList[0]))
}

func TestBuilderResults(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields()})
	b.SetResult(1, "first")
	commitRows(t, b, testRow{e: 1, host: "a", v: 1.0})
	b.StartSeries(SeriesHeader{Name: "open"})
	b.SetResult(2, "second")
	require.ErrorIs(t, b.AppendPoint(Point{Epoch: 1}), ErrInvalidRow)
	commitRows(t, b, testRow{e: 1, host: "a", v: 2.0})
	b.SetResult(1, "first")
	commitRows(t, b, testRow{e: 2, host: "a", v: 3.0})
	b.SetResult(3, "empty")

	ds, err := b.Finish()
	require.NoError(t, err)
	require.Len(t, ds.Results, 3)
	r1, r2, r3 := ds.Results[0], ds.Results[1], ds.Results[2]
	require.Equal(t, 1, r1.StatementID)
	require.Equal(t, "first", r1.Name)
	require.Len(t, r1.SeriesList, 2)
	require.Equal(t, "open", r1.SeriesList[1].Header.Name)
	require.Equal(t, []any{1.0, 3.0}, pointValues(r1.SeriesList[0]))
	require.Equal(t, 2, r2.StatementID)
	require.Equal(t, []any{2.0}, pointValues(r2.SeriesList[0]))
	require.Equal(t, "empty", r3.Name)
	require.NotNil(t, r3.SeriesList)
	require.Empty(t, r3.SeriesList)
}

func TestBuilderEmpty(t *testing.T) {
	ds, err := NewBuilder(testBuilderTRQ(), BuilderOptions{}).Finish()
	require.NoError(t, err)
	require.Len(t, ds.Results, 1)
	require.NotNil(t, ds.Results[0].SeriesList)
	require.Empty(t, ds.Results[0].SeriesList)
	require.Zero(t, ds.ValueCount())
}

func TestBuilderFinished(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{Fields: testBuilderFields()})
	_, err := b.Finish()
	require.NoError(t, err)
	_, err = b.Finish()
	require.ErrorIs(t, err, ErrBuilderFinished)
	b.SetResult(9, "late")
	b.StartSeries(SeriesHeader{Name: "late"})
	require.Len(t, b.results, 1)
	require.ErrorIs(t, b.AppendPoint(Point{}), ErrBuilderFinished)
	r := b.Row()
	r.SetEpoch(1)
	require.ErrorIs(t, r.Commit(), ErrBuilderFinished)
}

func TestBuilderValueChunks(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{})
	b.StartSeries(SeriesHeader{Name: "s"})
	for i := range 3 * minValueChunk {
		r := b.Row()
		r.SetEpoch(epoch.Epoch(i))
		r.AddValue(int64(i))
		require.NoError(t, r.Commit())
	}
	require.Equal(t, 2*minValueChunk, b.chunk)
	wide := b.Row()
	wide.SetEpoch(epoch.Epoch(3 * minValueChunk))
	for i := range 2 * maxValueChunk {
		wide.AddValue(i)
	}
	require.NoError(t, wide.Commit())
	ds, err := b.Finish()
	require.NoError(t, err)
	pts := ds.Results[0].SeriesList[0].Points
	require.Len(t, pts, 3*minValueChunk+1)
	for i, p := range pts[:3*minValueChunk] {
		require.Equal(t, []any{int64(i)}, p.Values)
		require.Equal(t, 1, cap(p.Values))
	}
	require.Len(t, pts[3*minValueChunk].Values, 2*maxValueChunk)
}

func TestPointSize(t *testing.T) {
	require.Equal(t, pointOverhead, PointSize(nil))
	values := []any{nil, "abc", []byte("ab"), true, int8(1), uint8(1), int16(1), uint16(1),
		int32(1), uint32(1), float32(1), int64(1), 1.0, uint64(1), 1}
	want := pointOverhead + len(values)*valueOverhead + 3 + 2 + 3 + 4 + 12 + 32
	require.Equal(t, want, PointSize(values))
}

func BenchmarkBuilderRows(b *testing.B) {
	hosts := [][]byte{[]byte("host-a"), []byte("host-b"), []byte("host-c"), []byte("host-d")}
	dc := []byte("dc-1")
	fields := testBuilderFields()
	b.ReportAllocs()
	for b.Loop() {
		bl := NewBuilder(nil, BuilderOptions{Fields: fields})
		for i := range 1000 {
			r := bl.Row()
			r.SetEpoch(epoch.Epoch(i / len(hosts)))
			r.SetTag(0, hosts[i%len(hosts)])
			r.SetTag(1, dc)
			r.AddValue(nil)
			if err := r.Commit(); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := bl.Finish(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBuilderEquivalentTagsShareSeries(t *testing.T) {
	b := NewBuilder(nil, BuilderOptions{
		Fields:    testBuilderFields(),
		TagString: func(_ timeseries.FieldDefinition, raw []byte) string { return strings.ToLower(string(raw)) },
	})
	commitRows(t, b,
		testRow{e: 1, host: "a", dc: "x", v: 1.0},
		testRow{e: 2, host: "A", dc: "X", v: 2.0},
		testRow{e: 3, host: "A", dc: "X", v: 3.0},
		testRow{e: 1, host: "b", dc: "x", v: 4.0},
	)
	// the second spelling is remembered, so its later rows skip the conversion
	require.Len(t, b.results[0].lookup, 3)
	ds, err := b.Finish()
	require.NoError(t, err)
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 2)
	require.Equal(t, Tags{"host": "a", "dc": "x"}, sl[0].Header.Tags)
	require.Equal(t, []any{1.0, 2.0, 3.0}, pointValues(sl[0]))
	requireSizes(t, sl[0])
	require.Equal(t, []any{4.0}, pointValues(sl[1]))
}

func TestBuilderDuplicateTagNamesShareSeries(t *testing.T) {
	fields := testBuilderFields()
	fields.Tags[1].Name = fields.Tags[0].Name
	b := NewBuilder(nil, BuilderOptions{Fields: fields})
	commitRows(t, b,
		testRow{e: 1, host: "a", dc: "x", v: 1.0},
		testRow{e: 2, host: "b", dc: "x", v: 2.0},
	)
	ds, err := b.Finish()
	require.NoError(t, err)
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 1)
	require.Equal(t, Tags{"host": "x"}, sl[0].Header.Tags)
	require.Equal(t, []epoch.Epoch{1, 2}, pointEpochs(sl[0]))
}

func TestBuilderStartSeriesReopensIdenticalHeader(t *testing.T) {
	value := timeseries.FieldDefinition{Name: "v", DataType: timeseries.String}
	h := SeriesHeader{Name: "up", Tags: Tags{"job": "a"}, ValueFieldsList: timeseries.FieldDefinitions{value}}
	b := NewBuilder(nil, BuilderOptions{})
	b.StartSeries(h)
	require.NoError(t, b.AppendPoint(Point{Epoch: 2, Values: []any{"2"}}))
	b.StartSeries(SeriesHeader{Name: "up", Tags: Tags{"job": "b"}, ValueFieldsList: h.ValueFieldsList})
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"1"}}))
	// attributes outside the header's identity, such as output positions, do not split a series
	moved := value
	moved.OutputPosition = 3
	b.StartSeries(SeriesHeader{Name: "up", Tags: Tags{"job": "a"}, ValueFieldsList: timeseries.FieldDefinitions{moved}})
	require.NoError(t, b.AppendPoint(Point{Epoch: 1, Values: []any{"1"}}))
	ds, err := b.Finish()
	require.NoError(t, err)
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 2)
	require.Equal(t, []epoch.Epoch{1, 2}, pointEpochs(sl[0]))
	require.Zero(t, sl[0].Header.ValueFieldsList[0].OutputPosition)
	require.Equal(t, []epoch.Epoch{1}, pointEpochs(sl[1]))
}

func TestSameSeries(t *testing.T) {
	fd := timeseries.FieldDefinition{Name: "v", DataType: timeseries.Float64}
	base := func() SeriesHeader {
		return SeriesHeader{
			Name: "n", QueryStatement: "q", Tags: Tags{"k": "v"}, TimestampField: fd,
			ValueFieldsList: timeseries.FieldDefinitions{fd}, UntrackedFieldsList: timeseries.FieldDefinitions{fd},
		}
	}
	a := base()
	b := base()
	require.True(t, sameSeries(&a, &b))
	b.TagFieldsList = timeseries.FieldDefinitions{fd}
	b.Size = 9
	require.True(t, sameSeries(&a, &b))
	for name, mutate := range map[string]func(*SeriesHeader){
		"name":      func(h *SeriesHeader) { h.Name = "x" },
		"query":     func(h *SeriesHeader) { h.QueryStatement = "x" },
		"tags":      func(h *SeriesHeader) { h.Tags = Tags{"k": "x"} },
		"timestamp": func(h *SeriesHeader) { h.TimestampField.DataType = timeseries.Int64 },
		"values":    func(h *SeriesHeader) { h.ValueFieldsList = nil },
		"value":     func(h *SeriesHeader) { h.ValueFieldsList[0].Name = "x" },
		"untracked": func(h *SeriesHeader) { h.UntrackedFieldsList = nil },
	} {
		b := base()
		mutate(&b)
		require.False(t, sameSeries(&a, &b), name)
	}
}
