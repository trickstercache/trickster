/*
 * Copyright 2026 The Trickster Authors
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
	"cmp"
	"io"
	"slices"
	"strings"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// legacyNativeMarshal is the native writer that sorted a reference to every point, kept as an oracle
func legacyNativeMarshal(ds *dataset.DataSet, plan *QueryPlan, writer io.Writer) error {
	points := legacyDruidPoints(ds, plan)
	var render func(*tbytes.ChunkWriter, []druidPoint)
	switch plan.QueryType() {
	case queryTimeseries:
		legacySortDruidPoints(points, plan.Descending(), false)
		render = legacyAppendTimeseries
	case queryGroupBy:
		legacySortDruidPoints(points, plan.Descending(), true)
		render = legacyAppendGroupBy
	case queryTopN:
		legacySortDruidPoints(points, plan.Descending(), true)
		render = legacyAppendTopN
	default:
		return timeseries.ErrUnknownFormat
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	if err := legacyCheckDruidValues(points, plan.QueryType()); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, '[')
	render(&cw, points)
	cw.Buf = append(cw.Buf, ']')
	return cw.Close()
}

func legacyDruidPoints(ds *dataset.DataSet, plan *QueryPlan) []druidPoint {
	count := 0
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series != nil {
				count += series.PointCount()
			}
		}
	}
	out := make([]druidPoint, 0, count)
	var layouts druidLayouts
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil || series.PointCount() == 0 {
				continue
			}
			s := layouts.series(series, plan)
			segs := series.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					dp := druidPoint{s: s, seg: &segs[k], row: i}
					if s.rank >= 0 {
						dp.rank = numericRank(legacyValue(&dp, s.rank))
					}
					out = append(out, dp)
				}
			}
		}
	}
	return out
}

func legacyCheckDruidValues(points []druidPoint, queryType string) error {
	for i := range points {
		dp := &points[i]
		cells := dp.s.event
		switch queryType {
		case queryTimeseries:
			cells = dp.s.result
		case queryGroupBy:
			if err := legacyCheckJSON(dp, dp.s.version); err != nil {
				return err
			}
		}
		for _, c := range cells {
			if err := legacyCheckJSON(dp, c.index); err != nil {
				return err
			}
		}
	}
	return nil
}

func legacyAppendTimeseries(cw *tbytes.ChunkWriter, points []druidPoint) {
	var tt timeText
	for i := range points {
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, `{"timestamp":`...)
		cw.Buf = tt.append(cw.Buf, points[i].epoch())
		cw.Buf = append(cw.Buf, `,"result":`...)
		cw.Buf = appendDruidObject(cw.Buf, &points[i], points[i].s.result)
		cw.Buf = append(cw.Buf, '}')
		cw.FlushIfFull()
	}
}

func legacyAppendGroupBy(cw *tbytes.ChunkWriter, points []druidPoint) {
	var tt timeText
	for i := range points {
		dp := &points[i]
		if i > 0 {
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, '{')
		if dp.has(dp.s.version) && dp.seg.KindAt(dp.s.version, dp.row) != dataset.KindNull {
			cw.Buf = append(cw.Buf, `"version":`...)
			cw.Buf, _ = dp.appendJSON(cw.Buf, dp.s.version)
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = append(cw.Buf, `"timestamp":`...)
		cw.Buf = tt.append(cw.Buf, dp.epoch())
		cw.Buf = append(cw.Buf, `,"event":`...)
		cw.Buf = appendDruidObject(cw.Buf, dp, dp.s.event)
		cw.Buf = append(cw.Buf, '}')
		cw.FlushIfFull()
	}
}

// one row per epoch, which holds its points' events in order
func legacyAppendTopN(cw *tbytes.ChunkWriter, points []druidPoint) {
	for i := range points {
		dp := &points[i]
		switch {
		case i == 0 || points[i-1].epoch() != dp.epoch():
			if i > 0 {
				cw.Buf = append(cw.Buf, "]},"...)
			}
			cw.Buf = append(cw.Buf, `{"timestamp":`...)
			cw.Buf = appendTimestamp(cw.Buf, dp.epoch())
			cw.Buf = append(cw.Buf, `,"result":[`...)
		default:
			cw.Buf = append(cw.Buf, ',')
		}
		cw.Buf = appendDruidObject(cw.Buf, dp, dp.s.event)
		cw.FlushIfFull()
	}
	if len(points) > 0 {
		cw.Buf = append(cw.Buf, "]}"...)
	}
}

func legacySortDruidPoints(points []druidPoint, descending, rank bool) {
	slices.SortStableFunc(points, func(a, b druidPoint) int {
		if a.epoch() != b.epoch() {
			if (a.epoch() < b.epoch()) != descending {
				return -1
			}
			return 1
		}
		if rank && a.rank != b.rank {
			if a.rank < b.rank {
				return -1
			}
			return 1
		}
		return strings.Compare(a.s.tagsKey, b.s.tagsKey)
	})
}

func legacyValue(dp *druidPoint, i int) any {
	if dp.has(i) {
		return dp.seg.Value(i, dp.row)
	}
	return nil
}

func legacyCheckJSON(dp *druidPoint, i int) error {
	if dp.has(i) {
		return checkDruidValue(dp.seg, i, dp.row)
	}
	return nil
}

// legacyCompareSQLTerms is legacyCompareSQLTerms as it boxed every value; it orders two rows by the ordering's terms, the first being term first of the order
func legacyCompareSQLTerms(a, b sqlRow, ordering []timeseries.OrderTerm, first int) int {
	for i, term := range ordering {
		t := first + i
		ai, bi := a.s.order[t], b.s.order[t]
		if ai < 0 || bi < 0 {
			continue
		}
		var comparison int
		if a.s.columns[ai].role == sqlColumnTimestamp && b.s.columns[bi].role == sqlColumnTimestamp {
			// times are never null; their text mixes millisecond and nanosecond forms, so compare epochs
			comparison = cmp.Compare(a.epoch(), b.epoch())
		} else {
			av, bv := a.value(ai), b.value(bi)
			if nulls, handled := compareSQLNulls(av, bv, term.NullsFirst); handled {
				if nulls != 0 {
					return nulls
				}
				continue
			}
			comparison = compareSQLValue(av, bv, false)
		}
		if comparison != 0 {
			if term.Descending {
				comparison = -comparison
			}
			return comparison
		}
	}
	return 0
}
