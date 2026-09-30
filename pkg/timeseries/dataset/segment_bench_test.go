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
	"runtime"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

// The benchmarks below run over 100 series of 1,000 rows, as a dashboard panel's cached entry holds.

const (
	benchSeriesCount = 100
	benchSeriesRows  = 1000
	benchBlobBytes   = 57
)

var benchStringValues = func() []string {
	out := make([]string, benchSeriesRows)
	for i := range out {
		out[i] = strconv.FormatFloat(float64(i%977)/7, 'f', -1, 64)
	}
	return out
}()

// the values as a decoder reads them, before they are strings
var benchByteValues = func() [][]byte {
	out := make([][]byte, benchSeriesRows)
	for i, v := range benchStringValues {
		out[i] = []byte(v)
	}
	return out
}()

// benchPoints returns a series of sorted points holding one numeric text value each
func benchPoints(offset int) Points {
	pts := make(Points, benchSeriesRows)
	for i := range pts {
		pts[i] = Point{Epoch: epoch.Epoch(offset+i) * 60e9, Values: []any{benchStringValues[i]}}
	}
	return pts
}

func BenchmarkBuildRowBlobs(b *testing.B) {
	blob := make([]byte, benchBlobBytes)
	tags := make([][]byte, benchSeriesCount)
	for i := range tags {
		tags[i] = []byte("host-" + strconv.Itoa(i))
	}
	b.Run("segments", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			l := NewColumnLog(DuplicatesKeep)
			for range benchSeriesCount {
				l.AddSeries(1)
			}
			for j := range benchSeriesRows {
				for i := range benchSeriesCount {
					l.AddBytes(blob)
					_ = l.Commit(i, epoch.Epoch(j))
				}
			}
			_, _ = l.Finish()
		}
	})
}

var benchRowFields = timeseries.SeriesFields{
	Timestamp: timeseries.FieldDefinition{Name: "time", Role: timeseries.RoleTimestamp},
	Tags:      timeseries.FieldDefinitions{{Name: "host", Role: timeseries.RoleTag}},
	Values:    timeseries.FieldDefinitions{{Name: "row", Role: timeseries.RoleValue}},
}

func BenchmarkBuildSeriesText(b *testing.B) {
	headers := make([]SeriesHeader, benchSeriesCount)
	for i := range headers {
		headers[i] = SeriesHeader{Name: "s" + strconv.Itoa(i), ValueFieldsList: benchRowFields.Values}
	}
	b.Run("segments", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			l := NewColumnLog(DuplicatesKeep)
			for range benchSeriesCount {
				s := l.AddSeries(1)
				for j := range benchSeriesRows {
					l.AddString(benchByteValues[j])
					_ = l.Commit(s, epoch.Epoch(j))
				}
			}
			_, _ = l.Finish()
		}
	})
}

func BenchmarkMergeOverlapping(b *testing.B) {
	// a cached series and a fetch that overlaps its last tenth, as a delta fill merges them
	cached, fetched := benchPoints(0), benchPoints(benchSeriesRows * 9 / 10)[:benchSeriesRows/5]
	cs, fs := Segments{segmentOf(cached, 1)}, Segments{segmentOf(fetched, 1)}
	for _, strategy := range []merge.Strategy{merge.StrategyDedup, merge.StrategySum} {
		name := "dedup"
		if strategy == merge.StrategySum {
			name = "sum"
		}
		opts := MergeOpts{SortPoints: true, Strategy: strategy}
		b.Run(name+"/segments", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = MergeSegments(cs, fs, opts)
			}
		})
	}
}

func BenchmarkViewSeries(b *testing.B) {
	pts := benchPoints(0)
	segs := Segments{segmentOf(pts, 1)}
	start, end := epoch.Epoch(benchSeriesRows/4)*60e9, epoch.Epoch(benchSeriesRows*3/4)*60e9
	b.Run("segments", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = segs.View(start, end)
		}
	})
}

func BenchmarkCodecSeries(b *testing.B) {
	pts := benchPoints(0)
	segs := Segments{segmentOf(pts, 1)}
	encodedSegments, _ := AppendSegments(nil, 0, segs)
	b.Run("encode/segments", func(b *testing.B) {
		b.ReportAllocs()
		buf := make([]byte, 0, len(encodedSegments))
		for b.Loop() {
			buf, _ = AppendSegments(buf[:0], 0, segs)
		}
	})
	b.Run("decode/segments", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _, _ = ReadSegments(encodedSegments, 0)
		}
	})
}

func BenchmarkRowsIteration(b *testing.B) {
	r := &Result{}
	lists := make([]Segments, benchSeriesCount)
	for i := range benchSeriesCount {
		pts := benchPoints(0)
		r.SeriesList = append(r.SeriesList, NewSeries(SeriesHeader{}, pts))
		lists[i] = Segments{segmentOf(pts, 1)}
	}
	b.Run("segments", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for row := range SegmentRows(lists, SegmentRowOrder{}) {
				_ = row.Epoch()
			}
		}
	})
	b.Run("result", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for row := range r.Rows(RowOrder{}) {
				_ = row.Epoch()
			}
		}
	})
}

// the copies BenchmarkRetainedGC keeps live, as a memory cache keeps its entries
const retainedCopies = 32

func BenchmarkRetainedGC(b *testing.B) {
	blob := make([]byte, benchBlobBytes)
	buildRows := func() any {
		bld := NewBuilder(nil, BuilderOptions{Fields: benchRowFields})
		for j := range benchSeriesRows {
			for i := range benchSeriesCount {
				row := bld.Row()
				row.SetEpoch(epoch.Epoch(j))
				row.SetTag(0, []byte{byte(i)})
				row.AddBytes(blob)
				_ = row.Commit()
			}
		}
		ds, _ := bld.Finish()
		return ds
	}
	buildLog := func() any {
		l := NewColumnLog(DuplicatesKeep)
		for range benchSeriesCount {
			l.AddSeries(1)
		}
		for j := range benchSeriesRows {
			for i := range benchSeriesCount {
				l.AddBytes(blob)
				_ = l.Commit(i, epoch.Epoch(j))
			}
		}
		segs, _ := l.Finish()
		return segs
	}
	headers := make([]SeriesHeader, benchSeriesCount)
	for i := range headers {
		headers[i] = SeriesHeader{Name: "s" + strconv.Itoa(i), ValueFieldsList: benchRowFields.Values}
	}
	// numeric text, one series at a time, as Prometheus responses hold
	buildText := func() any {
		bld := NewBuilder(nil, BuilderOptions{})
		for i := range benchSeriesCount {
			bld.StartSeries(headers[i])
			for j := range benchSeriesRows {
				row := bld.Row()
				row.SetEpoch(epoch.Epoch(j))
				row.AddString(benchByteValues[j])
				_ = row.Commit()
			}
		}
		ds, _ := bld.Finish()
		return ds
	}
	buildTextLog := func() any {
		l := NewColumnLog(DuplicatesKeep)
		for range benchSeriesCount {
			s := l.AddSeries(1)
			for j := range benchSeriesRows {
				l.AddString(benchByteValues[j])
				_ = l.Commit(s, epoch.Epoch(j))
			}
		}
		segs, _ := l.Finish()
		return segs
	}
	for _, tc := range []struct {
		name  string
		build func() any
	}{
		{"row-blobs/builder", buildRows},
		{"row-blobs/log", buildLog},
		{"text/builder", buildText},
		{"text/log", buildTextLog},
	} {
		b.Run(tc.name, func(b *testing.B) {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			kept := make([]any, retainedCopies)
			for i := range kept {
				kept[i] = tc.build()
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			points := float64(retainedCopies * benchSeriesCount * benchSeriesRows)
			for b.Loop() {
				runtime.GC()
			}
			b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/points, "live-B/point")
			b.ReportMetric((float64(after.HeapObjects)-float64(before.HeapObjects))/points, "objects/point")
			runtime.KeepAlive(kept)
		})
	}
}
