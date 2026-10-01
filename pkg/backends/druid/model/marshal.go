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
	"cmp"
	"io"
	"iter"
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
// and rank (-1 when none), and its tags as a sort key and the series' place in that order
type druidSeries struct {
	tagsKey  string
	tagOrder int
	result   []druidCell
	event    []druidCell
	version  int
	rank     int
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

// rankAt returns the rank of the row's series' rank field, and 0 when it has none
func rankAt(s *druidSeries, seg *dataset.Segment, row int) int64 {
	c := s.rank
	if c < 0 || c >= seg.NumCols() {
		return 0
	}
	if seg.KindAt(c, row) == dataset.KindInt64 {
		return seg.Int64(c, row)
	}
	return numericRank(seg.Value(c, row))
}

// appendJSON appends value i as Druid wrote it, or null when the row has none
func (dp *druidPoint) appendJSON(b []byte, i int) ([]byte, error) {
	if dp.has(i) {
		return appendDruidValue(b, dp.seg, i, dp.row)
	}
	return append(b, "null"...), nil
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
	o := newDruidOrder(ds, plan)
	if o == nil {
		return timeseries.ErrUnknownFormat
	}
	// the output is written as encoding/json would write it, so nothing is when a value can't be
	if err := o.check(); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(writer)
	cw.Buf = append(cw.Buf, '[')
	switch o.queryType {
	case queryTimeseries:
		appendTimeseries(&cw, o.points())
	case queryGroupBy:
		appendGroupBy(&cw, o.points())
	default:
		appendTopN(&cw, o.points())
	}
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

// druidOrder writes a native response's points: each series' layout, and the order Druid writes rows in,
// by time, then a groupBy's or topN's rank, then tags
type druidOrder struct {
	ds         *dataset.DataSet
	queryType  string
	descending bool
	// per result, per series; nil for a series without points
	layouts [][]*druidSeries
	// every series with points and its layout, stably in tag order
	byTags []taggedSeries
}

type taggedSeries struct {
	series *dataset.Series
	layout *druidSeries
}

func newDruidOrder(ds *dataset.DataSet, plan *QueryPlan) *druidOrder {
	switch plan.QueryType() {
	case queryTimeseries, queryGroupBy, queryTopN:
	default:
		return nil
	}
	o := &druidOrder{
		ds: ds, queryType: plan.QueryType(), descending: plan.Descending(),
		layouts: make([][]*druidSeries, len(ds.Results)),
	}
	var layouts druidLayouts
	for ri, result := range ds.Results {
		if result == nil {
			continue
		}
		o.layouts[ri] = make([]*druidSeries, len(result.SeriesList))
		for si, series := range result.SeriesList {
			if series == nil || series.PointCount() == 0 {
				continue
			}
			o.layouts[ri][si] = layouts.series(series, plan)
			o.byTags = append(o.byTags, taggedSeries{series, o.layouts[ri][si]})
		}
	}
	// a series' place among the tags, stably, orders its points as their tags and then their order do
	slices.SortStableFunc(o.byTags, func(a, b taggedSeries) int { return strings.Compare(a.layout.tagsKey, b.layout.tagsKey) })
	for i := range o.byTags {
		o.byTags[i].layout.tagOrder = i
	}
	return o
}

// ranked reports whether points of a time order by their rank
func (o *druidOrder) ranked() bool {
	return o.queryType != queryTimeseries
}

// check reports the first error writing a value would return, checking each column written
func (o *druidOrder) check() error {
	for ri, result := range o.ds.Results {
		if result == nil {
			continue
		}
		for si, series := range result.SeriesList {
			s := o.layouts[ri][si]
			if s == nil {
				continue
			}
			cells := s.event
			if o.queryType == queryTimeseries {
				cells = s.result
			}
			segs := series.Segments()
			for k := range segs {
				seg := &segs[k]
				if o.queryType == queryGroupBy && s.version >= 0 && s.version < seg.NumCols() {
					if err := checkDruidColumn(seg, s.version); err != nil {
						return err
					}
				}
				for _, c := range cells {
					if c.index >= 0 && c.index < seg.NumCols() {
						if err := checkDruidColumn(seg, c.index); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// points yields the points in Druid's order: merged from the series as they're read when they're sorted,
// and otherwise sorted
func (o *druidOrder) points() iter.Seq[*druidPoint] {
	if r, layouts, ok := o.merging(); ok {
		return func(yield func(*druidPoint) bool) {
			order := dataset.RowOrder{Descending: o.descending}
			// the series are in tag order, which the merge breaks ties by, but for a rank, and for the
			// stored order of a series' own ties, which a descending merge reads backward
			switch {
			case o.ranked():
				order.Compare = func(a, b dataset.Row) int {
					if c := cmp.Compare(rankAt(layouts[a.SeriesIndex], a.Seg, a.Index),
						rankAt(layouts[b.SeriesIndex], b.Seg, b.Index)); c != 0 {
						return c
					}
					return dataset.CompareStored(a, b)
				}
			case o.descending:
				order.Compare = dataset.CompareStored
			}
			var dp druidPoint
			for row := range r.Rows(order) {
				dp = druidPoint{s: layouts[row.SeriesIndex], seg: row.Seg, row: row.Index}
				if !yield(&dp) {
					return
				}
			}
		}
	}
	var points []druidPoint
	for ri, result := range o.ds.Results {
		if result == nil {
			continue
		}
		for si, series := range result.SeriesList {
			s := o.layouts[ri][si]
			if s == nil {
				continue
			}
			segs := series.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					points = append(points, druidPoint{s: s, seg: &segs[k], row: i, rank: rankAt(s, &segs[k], i)})
				}
			}
		}
	}
	sortDruidPoints(points, o.descending, o.ranked())
	return func(yield func(*druidPoint) bool) {
		for i := range points {
			if !yield(&points[i]) {
				return
			}
		}
	}
}

// merging returns every result's series in tag order, as one result to merge, and their layouts; it's
// false when a series isn't sorted
func (o *druidOrder) merging() (*dataset.Result, []*druidSeries, bool) {
	r := &dataset.Result{SeriesList: make(dataset.SeriesList, len(o.byTags))}
	layouts := make([]*druidSeries, len(o.byTags))
	for i, t := range o.byTags {
		if !t.series.IsSorted() {
			return nil, nil, false
		}
		r.SeriesList[i], layouts[i] = t.series, t.layout
	}
	return r, layouts, true
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

func appendTimeseries(cw *tbytes.ChunkWriter, points iter.Seq[*druidPoint]) {
	var tt timeText
	first := true
	for dp := range points {
		if !first {
			cw.Buf = append(cw.Buf, ',')
		}
		first = false
		cw.Buf = append(cw.Buf, `{"timestamp":`...)
		cw.Buf = tt.append(cw.Buf, dp.epoch())
		cw.Buf = append(cw.Buf, `,"result":`...)
		cw.Buf = appendDruidObject(cw.Buf, dp, dp.s.result)
		cw.Buf = append(cw.Buf, '}')
		cw.FlushIfFull()
	}
}

func appendGroupBy(cw *tbytes.ChunkWriter, points iter.Seq[*druidPoint]) {
	var tt timeText
	first := true
	for dp := range points {
		if !first {
			cw.Buf = append(cw.Buf, ',')
		}
		first = false
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
func appendTopN(cw *tbytes.ChunkWriter, points iter.Seq[*druidPoint]) {
	first, last := true, epoch.Epoch(0)
	for dp := range points {
		switch e := dp.epoch(); {
		case first || e != last:
			if !first {
				cw.Buf = append(cw.Buf, "]},"...)
			}
			cw.Buf = append(cw.Buf, `{"timestamp":`...)
			cw.Buf = appendTimestamp(cw.Buf, e)
			cw.Buf = append(cw.Buf, `,"result":[`...)
			last = e
		default:
			cw.Buf = append(cw.Buf, ',')
		}
		first = false
		cw.Buf = appendDruidObject(cw.Buf, dp, dp.s.event)
		cw.FlushIfFull()
	}
	if !first {
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
		return cmp.Compare(a.s.tagOrder, b.s.tagOrder)
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
