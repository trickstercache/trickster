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
	"slices"
	"strings"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// a series' output layout: each key's value in Druid's key order, the fields giving a point's version
// and rank (-1 when none), and its tags as a sort key
type druidSeries struct {
	tagsKey string
	result  []druidCell
	event   []druidCell
	version int
	rank    int
}

// a key, its name and quoted with its colon, the index of the value it takes, and its place among
// the keys as given
type druidCell struct {
	name  string
	key   string
	index int
	given int
}

// one output row: where its series holds it, and its rank
type druidPoint struct {
	s    *druidSeries
	seg  *dataset.Segment
	row  int
	rank int64
}

func (dp *druidPoint) epoch() epoch.Epoch {
	return dp.seg.Epoch(dp.row)
}

func (dp *druidPoint) has(i int) bool {
	return i >= 0 && i < dp.seg.NumCols()
}

// value returns value i, boxed, or nil when the row has none
func (dp *druidPoint) value(i int) any {
	if dp.has(i) {
		return dp.seg.Value(i, dp.row)
	}
	return nil
}

// appendJSON appends value i as Druid wrote it, or null when the row has none
func (dp *druidPoint) appendJSON(b []byte, i int) ([]byte, error) {
	if dp.has(i) {
		return appendDruidValue(b, dp.seg, i, dp.row)
	}
	return append(b, "null"...), nil
}

func (dp *druidPoint) checkJSON(i int) error {
	if dp.has(i) {
		return checkDruidValue(dp.seg, i, dp.row)
	}
	return nil
}

// MarshalTimeseries converts DataSet back into the native Druid response shape.
func MarshalTimeseries(ts timeseries.Timeseries, options *timeseries.RequestOptions,
	status int,
) ([]byte, error) {
	var buffer bytes.Buffer
	if err := MarshalTimeseriesWriter(ts, options, status, &buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// MarshalTimeseriesWriter writes DataSet in the native Druid response shape.
func MarshalTimeseriesWriter(ts timeseries.Timeseries, options *timeseries.RequestOptions,
	_ int, writer io.Writer,
) error {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return timeseries.ErrUnknownFormat
	}
	if plan := sqlPlanForMarshal(ds, options); plan != nil {
		return marshalSQLTimeseriesWriter(ds, plan, writer)
	}
	plan := planForMarshal(ds, options)
	if plan == nil {
		return timeseries.ErrUnknownFormat
	}
	points := druidPoints(ds, plan)
	var render func(*tbytes.ChunkWriter, []druidPoint)
	switch plan.QueryType() {
	case queryTimeseries:
		sortDruidPoints(points, plan.Descending(), false)
		render = appendTimeseries
	case queryGroupBy:
		sortDruidPoints(points, plan.Descending(), true)
		render = appendGroupBy
	case queryTopN:
		sortDruidPoints(points, plan.Descending(), true)
		render = appendTopN
	default:
		return timeseries.ErrUnknownFormat
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	if err := checkDruidValues(points, plan.QueryType()); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, '[')
	render(&cw, points)
	cw.Buf = append(cw.Buf, ']')
	return cw.Close()
}

func sqlPlanForMarshal(ds *dataset.DataSet, options *timeseries.RequestOptions) *SQLQueryPlan {
	if options != nil {
		if plan, ok := options.ProviderRequest.(*SQLQueryPlan); ok &&
			plan != nil && plan.Plan != nil {
			return plan
		}
	}
	if ds != nil && ds.TimeRangeQuery != nil {
		if plan, ok := ds.TimeRangeQuery.ParsedQuery.(*SQLQueryPlan); ok &&
			plan != nil && plan.Plan != nil {
			return plan
		}
	}
	return nil
}

func planForMarshal(ds *dataset.DataSet, options *timeseries.RequestOptions) *QueryPlan {
	if options != nil {
		if plan, ok := options.ProviderRequest.(*QueryPlan); ok {
			return plan
		}
	}
	if ds.TimeRangeQuery != nil {
		if plan, ok := ds.TimeRangeQuery.ParsedQuery.(*QueryPlan); ok {
			return plan
		}
	}
	return nil
}

func druidPoints(ds *dataset.DataSet, plan *QueryPlan) []druidPoint {
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
						dp.rank = numericRank(dp.value(s.rank))
					}
					out = append(out, dp)
				}
			}
		}
	}
	return out
}

// druidLayouts lays out series, reusing the keys of the last one when a series has its fields
type druidLayouts struct {
	fields timeseries.FieldDefinitions
	last   *druidSeries
}

func (l *druidLayouts) series(series *dataset.Series, plan *QueryPlan) *druidSeries {
	fields := series.Header.ValueFieldsList
	s := &druidSeries{tagsKey: series.Header.Tags.JSON(), version: -1, rank: -1}
	if l.last != nil && sameDruidFields(l.fields, fields) {
		s.result, s.event, s.version, s.rank = l.last.result, l.last.event, l.last.version, l.last.rank
		l.last = s
		return s
	}
	// a key's place is its first field's, and its value its last field's, as a Java map's put keeps
	for i, field := range fields {
		switch field.ProviderData1 {
		case fieldNativeVersion:
			s.version = i
		case fieldNativeRank:
			s.rank = i
		case fieldNativeDimension:
			s.event = putDruidCell(s.event, field.Name, i)
		default:
			s.event = putDruidCell(s.event, field.Name, i)
			s.result = putDruidCell(s.result, field.Name, i)
		}
	}
	// Druid sizes a timeseries' map for its aggregations, then adds the post-aggregations; a groupBy's
	// map has the default size, and a topN's order varies with Druid's own cache
	switch plan.QueryType() {
	case queryTimeseries:
		javaMapOrder(s.result, javaMapCapacityFor(min(plan.Aggregations(), len(s.result))))
	case queryGroupBy:
		javaMapOrder(s.event, javaMapCapacity)
	}
	l.fields, l.last = fields, s
	return s
}

// putDruidCell puts a key into cells, or gives its value to the key of that name
func putDruidCell(cells []druidCell, name string, index int) []druidCell {
	for j := range cells {
		if cells[j].name == name {
			cells[j].index = index
			return cells
		}
	}
	return append(cells, druidCell{name: name, key: string(append(appendJacksonString(nil, name), ':')), index: index})
}

func sameDruidFields(a, b timeseries.FieldDefinitions) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].ProviderData1 != b[i].ProviderData1 {
			return false
		}
	}
	return true
}

func checkDruidValues(points []druidPoint, queryType string) error {
	for i := range points {
		dp := &points[i]
		cells := dp.s.event
		switch queryType {
		case queryTimeseries:
			cells = dp.s.result
		case queryGroupBy:
			if err := dp.checkJSON(dp.s.version); err != nil {
				return err
			}
		}
		for _, c := range cells {
			if err := dp.checkJSON(c.index); err != nil {
				return err
			}
		}
	}
	return nil
}

// the values were checked, so none fails
func appendDruidObject(b []byte, dp *druidPoint, cells []druidCell) []byte {
	b = append(b, '{')
	for i, c := range cells {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, c.key...)
		b, _ = dp.appendJSON(b, c.index)
	}
	return append(b, '}')
}

func appendTimeseries(cw *tbytes.ChunkWriter, points []druidPoint) {
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

func appendGroupBy(cw *tbytes.ChunkWriter, points []druidPoint) {
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
func appendTopN(cw *tbytes.ChunkWriter, points []druidPoint) {
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

func sortDruidPoints(points []druidPoint, descending, rank bool) {
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

// appends the timestamp as a JSON string; neither layout writes anything JSON escapes
func appendTimestamp(b []byte, value epoch.Epoch) []byte {
	timestamp := time.Unix(0, int64(value)).UTC()
	b = append(b, '"')
	if timestamp.Nanosecond()%int(time.Millisecond) == 0 {
		b = timestamp.AppendFormat(b, druidMillisLayout)
	} else {
		b = timestamp.AppendFormat(b, time.RFC3339Nano)
	}
	return append(b, '"')
}

const druidMillisLayout = "2006-01-02T15:04:05.000Z"

func formatTimestamp(value epoch.Epoch) string {
	timestamp := time.Unix(0, int64(value)).UTC()
	if timestamp.Nanosecond()%int(time.Millisecond) == 0 {
		return timestamp.Format(druidMillisLayout)
	}
	return timestamp.Format(time.RFC3339Nano)
}

func numericRank(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		if v > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
