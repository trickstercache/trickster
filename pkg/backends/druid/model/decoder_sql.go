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
	"encoding/json/jsontext"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// the years a canonical SQL time holds whose nanoseconds an Epoch holds, which the fast path reads
const (
	minSQLYear = "1678"
	maxSQLYear = "2261"
)

// sqlDecoder builds a DataSet from a Druid SQL response of row objects, or of row arrays after their
// names: the plan's time, its groups as tags of their JSON, and the rest untyped values
type sqlDecoder struct {
	trq    *timeseries.TimeRangeQuery
	marker *SQLQueryPlan
	b      *dataset.Builder
	// the columns, the time's, the tags' and the values', and each column's index by name
	columns []string
	byName  map[string]int
	timeCol int
	tagCols []int
	valCols []int
	cells   [][]byte
	seen    []bool
	rows    int
	header  bool
	scratch []byte
	name    []byte
	// whether any row's time was a number of milliseconds, and any its text
	millis, text bool
	// a time or group column the columns lack, which fails the first row
	layoutErr error
}

func newSQLDecoder(trq *timeseries.TimeRangeQuery, marker *SQLQueryPlan) *sqlDecoder {
	return &sqlDecoder{trq: trq, marker: marker}
}

func (d *sqlDecoder) walk(dec *jsontext.Decoder) error {
	if dec.PeekKind() == 'n' {
		_, err := dec.ReadValue()
		return err
	}
	return stream.Array(dec, func() error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		d.rows++
		if d.marker.ResponseFormat() == SQLResponseArray {
			return d.arrayRow(raw)
		}
		return d.objectRow(raw)
	})
}

// objectRow reads a row object, whose names are the first row's, in any order and each once
func (d *sqlDecoder) objectRow(raw []byte) error {
	if raw[0] != '{' {
		return timeseries.ErrInvalidBody
	}
	if d.b == nil {
		var names []string
		members := stream.ObjectMembers(raw)
		for name, _, ok := members.Next(); ok; name, _, ok = members.Next() {
			names = append(names, string(stream.StringText(name, &d.name)))
		}
		if err := d.layout(names); err != nil {
			return err
		}
	}
	clear(d.cells)
	clear(d.seen)
	n := 0
	members := stream.ObjectMembers(raw)
	for name, value, ok := members.Next(); ok; name, value, ok = members.Next() {
		i, found := d.byName[string(stream.StringText(name, &d.name))]
		if !found || d.seen[i] {
			return timeseries.ErrInvalidBody
		}
		d.seen[i], d.cells[i] = true, value
		n++
	}
	if n != len(d.columns) {
		return timeseries.ErrInvalidBody
	}
	return d.row()
}

// arrayRow reads the row of column names, which only a response asked for with a header holds, and
// then each row of values
func (d *sqlDecoder) arrayRow(raw []byte) error {
	if !d.marker.Header() || raw[0] != '[' {
		return timeseries.ErrInvalidBody
	}
	elements := stream.ArrayElements(raw)
	if !d.header {
		d.header = true
		var names []string
		for e, ok := elements.Next(); ok; e, ok = elements.Next() {
			if e[0] != '"' {
				return timeseries.ErrInvalidBody
			}
			name := string(stream.StringText(e, &d.name))
			if name == "" || slices.Contains(names, name) {
				return timeseries.ErrInvalidBody
			}
			names = append(names, name)
		}
		if len(names) == 0 {
			return timeseries.ErrInvalidBody
		}
		return d.layout(names)
	}
	n := 0
	for e, ok := elements.Next(); ok; e, ok = elements.Next() {
		if n == len(d.cells) {
			return timeseries.ErrInvalidBody
		}
		d.cells[n] = e
		n++
	}
	if n != len(d.cells) {
		return timeseries.ErrInvalidBody
	}
	return d.row()
}

// layout lays out the columns: the plan's time and groups, matched exactly or else ignoring case,
// and the values, each column once
func (d *sqlDecoder) layout(columns []string) error {
	plan := d.marker.Plan
	if expected := d.marker.OutputColumns(); len(columns) > 0 && len(expected) > 0 && !sameSQLColumns(expected, columns) {
		return timeseries.ErrInvalidBody
	}
	d.columns = columns
	d.byName = make(map[string]int, len(columns))
	for i, name := range columns {
		d.byName[name] = i
	}
	d.cells, d.seen = make([][]byte, len(columns)), make([]bool, len(columns))
	if d.timeCol = sqlColumnIndex(columns, plan.OutputColumn); d.timeCol < 0 {
		d.layoutErr = timeseries.ErrInvalidBody
		return nil
	}
	used := []int{d.timeCol}
	fields := timeseries.SeriesFields{Timestamp: timeseries.FieldDefinition{
		Name: columns[d.timeCol], DataType: timeseries.DateTimeRFC3339Nano,
		Role: timeseries.RoleTimestamp, OutputPosition: d.timeCol,
	}}
	for _, name := range plan.GroupColumns {
		i := sqlColumnIndex(columns, name)
		if i < 0 || slices.Contains(used, i) {
			d.layoutErr = timeseries.ErrInvalidBody
			return nil
		}
		used = append(used, i)
		d.tagCols = append(d.tagCols, i)
		fields.Tags = append(fields.Tags, timeseries.FieldDefinition{
			Name: columns[i], DataType: timeseries.String, Role: timeseries.RoleTag, OutputPosition: i,
		})
	}
	for i, name := range columns {
		if slices.Contains(used, i) {
			continue
		}
		d.valCols = append(d.valCols, i)
		// the response holds no types, so a field is untyped in every extent, and each value keeps its own
		fields.Values = append(fields.Values, timeseries.FieldDefinition{
			Name: name, DataType: timeseries.Unknown, Role: timeseries.RoleValue, OutputPosition: i,
		})
	}
	d.b = dataset.NewBuilder(d.trq, dataset.BuilderOptions{
		Fields: fields, SeriesName: sqlResultName,
		QueryStatement: d.trq.Statement,
	})
	return nil
}

// row adds the row the cells hold; a time that doesn't read fails the response
func (d *sqlDecoder) row() error {
	if d.layoutErr != nil {
		return d.layoutErr
	}
	e, err := d.sqlTime(d.cells[d.timeCol])
	if err != nil {
		return err
	}
	rb := d.b.Row()
	rb.SetEpoch(e)
	for i, c := range d.tagCols {
		rb.SetTag(i, d.cells[c])
	}
	for _, c := range d.valCols {
		v := parseJSONValue(d.cells[c], &d.scratch)
		v.add(rb)
	}
	return rb.Commit()
}

// sqlTime reads a row's time as parseDruidSQLTimestamp does, a canonical text without a zone
// directly
func (d *sqlDecoder) sqlTime(raw []byte) (epoch.Epoch, error) {
	v := parseJSONValue(raw, &d.scratch)
	switch v.dt {
	case timeseries.String:
		d.text = true
		if len(v.text) >= len(minSQLYear) && string(v.text[:4]) >= minSQLYear && string(v.text[:4]) <= maxSQLYear {
			if e, ok := epoch.ParseCanonicalTime(v.text, true); ok {
				return e, nil
			}
			if e, ok := epoch.ParseCanonicalTime(v.text, false); ok {
				return e, nil
			}
			if e, ok := epoch.ParseSQLDateTime(v.text); ok {
				return e, nil
			}
		}
		return parseDruidSQLTimestamp(string(v.text))
	case timeseries.Int64:
		d.millis = true
		return parseDruidSQLTimestamp(v.i)
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

func (d *sqlDecoder) finish() (timeseries.Timeseries, error) {
	if d.marker.ResponseFormat() == SQLResponseArray && d.rows == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	extents := timeseries.ExtentList{d.trq.Extent}
	if d.b == nil || d.layoutErr != nil {
		return &dataset.DataSet{
			Status: dataSetStatusSuccess, TimeRangeQuery: d.trq, ExtentList: extents,
			Results: dataset.Results{&dataset.Result{Name: sqlResultName, SeriesList: dataset.SeriesList{}}},
		}, nil
	}
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	r := ds.Results[0]
	r.Name = sqlResultName
	if r.SeriesList == nil {
		r.SeriesList = dataset.SeriesList{}
	}
	// a time column of milliseconds is written as one
	if d.millis && !d.text {
		for _, s := range r.SeriesList {
			s.Header.TimestampField.DataType = timeseries.DateTimeUnixMilli
		}
	}
	slices.SortFunc(r.SeriesList, func(a, b *dataset.Series) int {
		return strings.Compare(a.Header.Tags.JSON(), b.Header.Tags.JSON())
	})
	return &dataset.DataSet{
		Status: dataSetStatusSuccess, Results: dataset.Results{r}, TimeRangeQuery: d.trq,
		ExtentList: extents,
	}, nil
}
