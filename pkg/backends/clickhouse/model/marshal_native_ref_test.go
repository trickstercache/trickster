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
	"bytes"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// the time column types of random DataSets, some of which the old marshaler couldn't write
var nativeTimeTypes = []struct {
	typ string
	dt  timeseries.FieldDataType
}{
	{"DateTime", timeseries.DateTimeSQL},
	{"Date", timeseries.DateSQL},
	{"DateTime64(0)", timeseries.DateTimeSQL},
	{"DateTime64(3)", timeseries.DateTimeSQL},
	{"DateTime64(9, 'UTC')", timeseries.DateTimeSQL},
	{"DateTime64(6, 'Asia/Tokyo')", timeseries.DateTimeSQL},
	{"UInt64", timeseries.DateTimeUnixMilli},
	{"UInt32", timeseries.DateTimeUnixSecs},
	{"Int64", timeseries.DateTimeUnixNano},
	{"Int32", timeseries.DateTimeUnixMicro},
	{"UInt16", timeseries.DateTimeUnixSecs},
	{"Float64", timeseries.DateTimeUnixSecs},
	{"DateTime('UTC')", timeseries.DateTimeSQL},
}

var nativeTagTypes = []string{"String", "Nullable(String)", "LowCardinality(String)",
	"LowCardinality(Nullable(String))", "UInt8", "FixedString(3)"}

func pick[T any](rng *weaktest.Rand, vs ...T) T {
	return vs[rng.IntN(len(vs))]
}

// the value column types of random DataSets, each with the values a column of it can hold
var nativeValueTypes = []struct {
	typ   string
	value func(rng *weaktest.Rand) any
}{
	{"Float64", func(rng *weaktest.Rand) any {
		return pick[any](rng, rng.NormFloat64(), math.NaN(), math.Float64frombits(0x7ff8000000000abc), math.Inf(-1),
			math.Copysign(0, -1), 1.5)
	}},
	{"Nullable(Float64)", func(rng *weaktest.Rand) any { return pick[any](rng, rng.Float64(), nil) }},
	{"Int8", func(rng *weaktest.Rand) any { return int64(rng.IntN(256) - 128) }},
	{"Int16", func(rng *weaktest.Rand) any { return int64(rng.IntN(65536) - 32768) }},
	{"Int32", func(rng *weaktest.Rand) any { return pick[any](rng, int64(rng.Int32()), int64(-5), nil) }},
	{"Int64", func(rng *weaktest.Rand) any { return pick[any](rng, rng.Int64(), int64(math.MinInt64), nil) }},
	{"UInt8", func(rng *weaktest.Rand) any { return uint64(rng.IntN(256)) }},
	{"UInt16", func(rng *weaktest.Rand) any { return uint64(rng.IntN(65536)) }},
	{"UInt32", func(rng *weaktest.Rand) any { return uint64(rng.Uint32()) }},
	{"UInt64", func(rng *weaktest.Rand) any { return pick[any](rng, rng.Uint64(), uint64(math.MaxUint64), nil) }},
	{"Nullable(Int32)", func(rng *weaktest.Rand) any { return pick[any](rng, int64(rng.Int32()), nil) }},
	{"Nullable(UInt8)", func(rng *weaktest.Rand) any { return pick[any](rng, uint64(rng.IntN(256)), nil) }},
	{"Bool", func(rng *weaktest.Rand) any { return pick[any](rng, true, false, nil) }},
	{"Nullable(Bool)", func(rng *weaktest.Rand) any { return pick[any](rng, true, false, nil) }},
	{"String", func(rng *weaktest.Rand) any {
		return pick[any](rng, "a", "", "long value "+strconv.Itoa(rng.IntN(9)), nil)
	}},
	{"Nullable(String)", func(rng *weaktest.Rand) any { return pick[any](rng, "x", "", nil) }},
	{"LowCardinality(String)", func(rng *weaktest.Rand) any { return pick[any](rng, "p", "q", "", nil) }},
	{"LowCardinality(Nullable(String))", func(rng *weaktest.Rand) any { return pick[any](rng, "p", "", nil) }},
	{"Float32", func(rng *weaktest.Rand) any { return rng.Float64() }},
	{"Decimal(9, 2)", func(rng *weaktest.Rand) any { return float64(rng.IntN(1e5)) / 100 }},
	{"DateTime", func(rng *weaktest.Rand) any { return "2024-01-01 00:00:00" }},
	{"Array(UInt8)", func(rng *weaktest.Rand) any { return nil }},
}

// values a column's writer doesn't take, which leave it boxed
var outOfTypeValues = []any{int64(1 << 40), uint64(1 << 63), -1.5, "7", true, uint64(3), int64(3), int64(128),
	int64(-129), int64(32768), int64(-32769), int64(1 << 31), int64(-1<<31 - 1), uint64(256), uint64(65536),
	uint64(1 << 32), uint64(math.MaxInt64 + 1)}

// randomNativeDataSet builds a DataSet as a ClickHouse decoder would, with fields of random types
func randomNativeDataSet(rng *weaktest.Rand) *dataset.DataSet {
	tt := nativeTimeTypes[rng.IntN(len(nativeTimeTypes))]
	fields := timeseries.FieldDefinitions{{Name: "t", Role: timeseries.RoleTimestamp, SDataType: tt.typ, DataType: tt.dt}}
	var tags, vals timeseries.FieldDefinitions
	for i := range rng.IntN(3) {
		tags = append(tags, timeseries.FieldDefinition{Name: "tag" + strconv.Itoa(i), Role: timeseries.RoleTag,
			SDataType: pick(rng, nativeTagTypes...), OutputPosition: len(fields) + i})
	}
	kinds := make([]int, 1+rng.IntN(3))
	for i := range kinds {
		kinds[i] = rng.IntN(len(nativeValueTypes))
		vals = append(vals, timeseries.FieldDefinition{Name: "v" + strconv.Itoa(i), Role: timeseries.RoleValue,
			SDataType: nativeValueTypes[kinds[i]].typ, OutputPosition: len(fields) + len(tags) + i})
	}
	var series dataset.SeriesList
	for s := range 1 + rng.IntN(6) {
		t := dataset.Tags{}
		for _, tag := range tags {
			// a tag left out is NULL
			if v := pick(rng, "h"+strconv.Itoa(s%3), "", "-", "7"); v != "-" {
				t[tag.Name] = v
			}
		}
		h := dataset.SeriesHeader{Name: strconv.Itoa(s), Tags: t, TimestampField: fields[0], TagFieldsList: tags,
			ValueFieldsList: vals}
		// a series without one of the fields can't be written
		if rng.IntN(40) == 0 {
			h.ValueFieldsList = vals[:len(vals)-1]
		}
		var pts dataset.Points
		base := pick(rng, int64(1700000000e9), int64(-86400e9*400), int64(math.MaxInt64-1e12), rng.Int64())
		for p := range rng.IntN(20) {
			values := make([]any, len(h.ValueFieldsList))
			for j := range values {
				values[j] = nativeValueTypes[kinds[j]].value(rng)
				if rng.IntN(60) == 0 {
					values[j] = pick(rng, outOfTypeValues...)
				}
			}
			at := base + int64(p)*pick(rng, int64(1), int64(1e6)+7, int64(60e9), int64(86400e9))
			pts = append(pts, dataset.Point{Epoch: epoch.Epoch(at), Values: values})
		}
		// the series merge when the rows are written, whatever order they're held in
		if rng.IntN(4) == 0 {
			rng.Shuffle(len(pts), func(a, b int) { pts[a], pts[b] = pts[b], pts[a] })
		}
		series = append(series, dataset.NewSeries(h, pts))
	}
	return &dataset.DataSet{Results: dataset.Results{{SeriesList: series}}}
}

// legacyComparable reports whether the old marshaler wrote ds as the new one does: it wrote a NULL tag
// as "", repeated a nullable dictionary's entries, and shifted (or failed on) a zoned time
func legacyComparable(ds *dataset.DataSet) bool {
	fds, _, _, tfd := ds.FieldDefinitions()
	if f := newOutField(&tfd, &FormatOptions{}); f.class == classDateTime && strings.Contains(tfd.SDataType, "'") {
		return false
	}
	for _, fd := range fds {
		if strings.Contains(fd.SDataType, "LowCardinality(Nullable(") {
			return false
		}
		if fd.Role != timeseries.RoleTag || !nullableType(fd.SDataType) {
			continue
		}
		for _, s := range ds.Results[0].SeriesList {
			if _, ok := s.Header.Tags[fd.Name]; !ok {
				return false
			}
		}
	}
	return true
}

func requireNativeMatchesLegacy(t *testing.T, ds *dataset.DataSet, revision uint64) {
	t.Helper()
	rlo := &timeseries.RequestOptions{ProviderRequest: FormatOptions{Revision: revision}}
	var want, got bytes.Buffer
	werr := legacyMarshalTimeseriesNative(&want, ds, rlo)
	err := marshalTimeseriesNative(&got, ds, rlo)
	// a response without rows is an empty body, as ClickHouse's
	if len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		require.NoError(t, err)
		require.Zero(t, got.Len())
		return
	}
	if !legacyComparable(ds) {
		// what's written reads back, which ClickHouse's own reader checks row by row in the live tests
		if err == nil && got.Len() > 0 {
			_, tags, _, tfd := ds.FieldDefinitions()
			names := make([]string, len(tags))
			for i, fd := range tags {
				names[i] = fd.Name
			}
			_, derr := UnmarshalTimeseriesNative(got.Bytes(), decoderTRQ(tfd.Name, tfd.DataType, names...))
			require.NoError(t, derr)
		}
		return
	}
	if werr != nil || err != nil {
		require.Error(t, err, "legacy failed with %v", werr)
		require.Error(t, werr, "legacy succeeded")
		require.Equal(t, werr.Error(), err.Error())
		require.Zero(t, got.Len())
		return
	}
	require.Equal(t, want.Bytes(), got.Bytes())
}

func TestMarshalNativeMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(12, 12)
	for trial := range 600 {
		ds := randomNativeDataSet(rng)
		for _, revision := range []uint64{0, server.ServerRevision} {
			t.Run(strconv.Itoa(trial)+"/"+strconv.FormatUint(revision, 10), func(t *testing.T) {
				requireNativeMatchesLegacy(t, ds, revision)
			})
		}
	}
	// decoded responses, which hold every type a decoder gives a column
	for name, c := range nativeBodies {
		ts, err := UnmarshalTimeseriesNative(nativeBody(t, c.revision, c.blocks...), c.trq)
		require.NoError(t, err)
		t.Run(name, func(t *testing.T) { requireNativeMatchesLegacy(t, ts.(*dataset.DataSet), server.ServerRevision) })
	}
	for name, c := range tsvBodies {
		ts, err := UnmarshalTimeseries([]byte(c.body), c.trq)
		require.NoError(t, err)
		t.Run(name, func(t *testing.T) { requireNativeMatchesLegacy(t, ts.(*dataset.DataSet), server.ServerRevision) })
	}
}

func TestMarshalNativeLowCardinalityKeyWidths(t *testing.T) {
	// a dictionary's keys widen as its values outgrow a byte, then two
	for _, distinct := range []int{254, 255, 65534, 65535} {
		fields := timeseries.FieldDefinitions{
			{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime", DataType: timeseries.DateTimeSQL},
			{Name: "v", Role: timeseries.RoleValue, SDataType: "LowCardinality(String)", OutputPosition: 1},
		}
		pts := make(dataset.Points, distinct+2)
		for i := range pts {
			pts[i] = dataset.Point{Epoch: epoch.Epoch(int64(i) * 1e9), Values: []any{strconv.Itoa(i % distinct)}}
		}
		s := dataset.NewSeries(dataset.SeriesHeader{TimestampField: fields[0], ValueFieldsList: fields[1:]}, pts)
		requireNativeMatchesLegacy(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{s}}}},
			server.ServerRevision)
	}
}

func BenchmarkMarshalNativeLegacy(b *testing.B) {
	ts, err := UnmarshalTimeseries(tsvBody(100, 1000), sqlTRQ())
	require.NoError(b, err)
	ds := ts.(*dataset.DataSet)
	rlo := &timeseries.RequestOptions{}
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := legacyMarshalTimeseriesNative(io.Discard, ds, rlo); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := marshalTimeseriesNative(io.Discard, ds, rlo); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestMarshalNativeRulings(t *testing.T) {
	at := time.Date(2026, 11, 1, 5, 30, 0, 123456000, time.UTC)
	fields := func(timeType string) timeseries.FieldDefinitions {
		return timeseries.FieldDefinitions{
			{Name: "t", Role: timeseries.RoleTimestamp, SDataType: timeType, DataType: timeseries.DateTimeSQL},
			{Name: "host", Role: timeseries.RoleTag, SDataType: "Nullable(String)", OutputPosition: 1},
			{Name: "dc", Role: timeseries.RoleTag, SDataType: "LowCardinality(Nullable(String))", OutputPosition: 2},
			{Name: "lc", Role: timeseries.RoleValue, SDataType: "LowCardinality(Nullable(String))", OutputPosition: 3},
			{Name: "dt", Role: timeseries.RoleValue, SDataType: "Nullable(DateTime64(3, 'Asia/Tokyo'))", OutputPosition: 4},
			{Name: "m", Role: timeseries.RoleValue, SDataType: "Map(String, UInt8)", OutputPosition: 5},
		}
	}
	for _, timeType := range []string{"DateTime('Asia/Tokyo')", "DateTime64(6, 'Asia/Tokyo')", "DateTime64(3, 'UTC')"} {
		fds := fields(timeType)
		var series dataset.SeriesList
		for s, tags := range []dataset.Tags{{"host": "a", "dc": "x"}, {}} {
			var pts dataset.Points
			for i := range 10 {
				pts = append(pts, dataset.Point{Epoch: epoch.Epoch(at.Add(time.Duration(i) * time.Minute).UnixNano()),
					Values: []any{"pqrstuvw", pick[any](weaktest.NewRand(uint64(i), uint64(s)), "2026-11-01 05:30:00.250", nil),
						"{'z':1,'a':2}"}})
			}
			series = append(series, dataset.NewSeries(dataset.SeriesHeader{Tags: tags, TimestampField: fds[0],
				TagFieldsList: fds[1:3], ValueFieldsList: fds[3:]}, pts))
		}
		ds := &dataset.DataSet{Results: dataset.Results{{SeriesList: series}}}
		var out bytes.Buffer
		require.NoError(t, marshalTimeseriesNative(&out, ds, &timeseries.RequestOptions{}), timeType)
		// a nullable dictionary holds each value once
		require.Equal(t, 1, bytes.Count(out.Bytes(), []byte("pqrstuvw")), timeType)
		back := decodeNative(t, out.Bytes(), decoderTRQ("t", timeseries.DateTimeSQL, "host", "dc"))
		sl := back.Results[0].SeriesList
		require.Len(t, sl, 2, timeType)
		// times are the ticks, whatever the zone; a NULL tag stays one
		require.Equal(t, dataset.Tags{"host": "a", "dc": "x"}, sl[0].Header.Tags)
		require.Equal(t, dataset.Tags{}, sl[1].Header.Tags)
		for _, s := range sl {
			pts := dspoints.Of(s)
			require.Len(t, pts, 10)
			want := at.Truncate(time.Second)
			switch timeType {
			case "DateTime64(6, 'Asia/Tokyo')":
				want = at.Truncate(time.Microsecond)
			case "DateTime64(3, 'UTC')":
				want = at.Truncate(time.Millisecond)
			}
			require.Equal(t, epoch.Epoch(want.UnixNano()), pts[0].Epoch, timeType)
			for _, p := range pts {
				require.Equal(t, "pqrstuvw", p.Values[0])
				if p.Values[1] != nil {
					require.Equal(t, "2026-11-01 05:30:00.250", p.Values[1])
				}
				require.Equal(t, "{'z':1,'a':2}", p.Values[2])
			}
		}
	}
}
