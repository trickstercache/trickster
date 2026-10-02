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
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// the members of a native response's rows
const (
	memberResult  = "result"
	memberEvent   = "event"
	memberVersion = "version"
)

// the JSON of a null, as a missing dimension's tag
var jsonNull = []byte(jsonNullText)

// jsonValue is a JSON value as Druid wrote it: an integer that fits an int64 as one, a string as its
// text, and any other number, object or array as its JSON, which it's written back as
type jsonValue struct {
	dt   timeseries.FieldDataType
	i    int64
	b    bool
	text []byte
}

// parseJSONValue reads a JSON value, a missing member's nil as null; any number but an int64 keeps its
// text, typed Uint64 or Float64, as does an object or array, typed Unknown
func parseJSONValue(raw []byte, scratch *[]byte) jsonValue {
	if len(raw) == 0 {
		return jsonValue{dt: timeseries.Null}
	}
	switch raw[0] {
	case 'n':
		return jsonValue{dt: timeseries.Null}
	case 't', 'f':
		return jsonValue{dt: timeseries.Bool, b: raw[0] == 't'}
	case '"':
		return jsonValue{dt: timeseries.String, text: stream.StringText(raw, scratch)}
	case '{', '[':
		return jsonValue{dt: timeseries.Unknown, text: raw}
	}
	// only an integer's literal parses as one, and a failed parse costs an error
	if stream.IsIntegerLiteral(raw) {
		if i, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
			return jsonValue{dt: timeseries.Int64, i: i}
		}
		if _, err := strconv.ParseUint(string(raw), 10, 64); err == nil {
			return jsonValue{dt: timeseries.Uint64, text: raw}
		}
	}
	return jsonValue{dt: timeseries.Float64, text: raw}
}

// add adds the value to a row: a number's text as a KindNumber, and an object's or array's JSON as
// KindBytes, which the writers copy as they are
func (v *jsonValue) add(rb *dataset.RowBuilder) {
	switch v.dt {
	case timeseries.Null:
		rb.AddNull()
	case timeseries.Bool:
		rb.AddBool(v.b)
	case timeseries.String:
		rb.AddString(v.text)
	case timeseries.Int64:
		rb.AddInt64(v.i)
	case timeseries.Float64, timeseries.Uint64:
		rb.AddNumber(v.text)
	default:
		rb.AddBytes(v.text)
	}
}

// typeTracker merges a field's types over its values as responseFieldTypes did: a type replaces
// none, null or unknown, and a different one makes the field unknown
type typeTracker struct {
	dt  timeseries.FieldDataType
	set bool
}

func (t *typeTracker) add(dt timeseries.FieldDataType) {
	switch {
	case !t.set || t.dt == timeseries.Null || t.dt == timeseries.Unknown:
		t.dt, t.set = dt, true
	case dt != timeseries.Null && dt != timeseries.Unknown && dt != t.dt:
		t.dt = timeseries.Unknown
	}
}

// nativeDecoder builds a DataSet from a native timeseries, groupBy or topN response, a row per point:
// its dimensions as tags and values, its version and rank, then its values, declared ones first
type nativeDecoder struct {
	trq  *timeseries.TimeRangeQuery
	plan *QueryPlan
	dims []string
	b    *dataset.Builder
	// the value fields: the declared ones, then those the rows add, by name
	values []string
	byName map[string]int
	// each field's type, by name, and a row's dimension and value cells
	types   map[string]*typeTracker
	dimRaw  [][]byte
	cells   [][]byte
	version []byte
	// a groupBy bucket's rows so far, by epoch
	ranks         map[epoch.Epoch]int64
	scratch, name []byte
}

func newNativeDecoder(trq *timeseries.TimeRangeQuery, plan *QueryPlan) *nativeDecoder {
	d := &nativeDecoder{
		trq: trq, plan: plan, dims: plan.Dimensions(), values: plan.ValueFields(),
		types: map[string]*typeTracker{},
	}
	d.byName = make(map[string]int, len(d.values))
	for i, name := range d.values {
		d.byName[name] = i
	}
	d.dimRaw, d.cells = make([][]byte, len(d.dims)), make([][]byte, len(d.values))
	if plan.QueryType() == queryGroupBy {
		d.ranks = map[epoch.Epoch]int64{}
	}
	fields := timeseries.SeriesFields{Tags: make(timeseries.FieldDefinitions, len(d.dims))}
	for i, name := range d.dims {
		fields.Tags[i] = timeseries.FieldDefinition{Name: name, Role: timeseries.RoleTag}
	}
	// the dimensions, version and rank, and declared values; the headers are set as the build ends
	for range len(d.dims) + d.extra() + len(d.values) {
		fields.Values = append(fields.Values, timeseries.FieldDefinition{Role: timeseries.RoleValue})
	}
	d.b = dataset.NewBuilder(trq, dataset.BuilderOptions{Fields: fields})
	return d
}

// extra returns the count of fields between the dimensions and the values: a groupBy's version and
// rank, or a topN's rank
func (d *nativeDecoder) extra() int {
	switch d.plan.QueryType() {
	case queryGroupBy:
		return 2
	case queryTopN:
		return 1
	}
	return 0
}

func (d *nativeDecoder) typeOf(name string) *typeTracker {
	t, ok := d.types[name]
	if !ok {
		t = &typeTracker{}
		d.types[name] = t
	}
	return t
}

// walk reads the response, an array of rows; a null response holds none
func (d *nativeDecoder) walk(dec *jsontext.Decoder) error {
	if dec.PeekKind() == 'n' {
		_, err := dec.ReadValue()
		return err
	}
	return stream.Array(dec, func() error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		return d.element(raw)
	})
}

// element reads one row of the response: its time, and its result, event or topN rows
func (d *nativeDecoder) element(raw []byte) error {
	if raw[0] != '{' {
		return timeseries.ErrInvalidBody
	}
	var timestamp, body []byte
	d.version = nil
	members := stream.ObjectMembers(raw)
	for name, value, ok := members.Next(); ok; name, value, ok = members.Next() {
		// a repeated member's last value is the one kept
		switch string(stream.StringText(name, &d.name)) {
		case fieldTimestamp:
			timestamp = value
		case memberResult:
			if d.plan.QueryType() != queryGroupBy {
				body = value
			}
		case memberEvent:
			if d.plan.QueryType() == queryGroupBy {
				body = value
			}
		case memberVersion:
			d.version = value
		}
	}
	if len(timestamp) == 0 || timestamp[0] != '"' {
		return timeseries.ErrInvalidBody
	}
	e, err := epoch.ParseRFC3339(stream.StringText(timestamp, &d.scratch), time.RFC3339Nano)
	if err != nil {
		return err
	}
	switch d.plan.QueryType() {
	case queryTopN:
		if len(body) == 0 || body[0] != '[' {
			return timeseries.ErrInvalidBody
		}
		rows := stream.ArrayElements(body)
		var rank int64
		for row, ok := rows.Next(); ok; row, ok = rows.Next() {
			if err := d.point(e, row, rank); err != nil {
				return err
			}
			rank++
		}
		return nil
	case queryGroupBy:
		rank := d.ranks[e]
		d.ranks[e] = rank + 1
		return d.point(e, body, rank)
	}
	return d.point(e, body, 0)
}

// point adds one point: the members of its result or event object, its dimensions split out of them
func (d *nativeDecoder) point(e epoch.Epoch, body []byte, rank int64) error {
	if len(body) == 0 || body[0] != '{' {
		return timeseries.ErrInvalidBody
	}
	clear(d.dimRaw)
	clear(d.cells)
	split := d.plan.QueryType() != queryTimeseries
	members := stream.ObjectMembers(body)
	for name, value, ok := members.Next(); ok; name, value, ok = members.Next() {
		key := stream.StringText(name, &d.name)
		if split {
			if i := slices.Index(d.dims, string(key)); i >= 0 {
				d.dimRaw[i] = value
				continue
			}
		}
		i, found := d.byName[string(key)]
		if !found {
			i = d.addValue(string(key))
		}
		d.cells[i] = value
	}
	rb := d.b.Row()
	rb.SetEpoch(e)
	for i, name := range d.dims {
		v := parseJSONValue(d.dimRaw[i], &d.scratch)
		d.typeOf(name).add(v.dt)
		// a dimension's tag is its text, or the JSON Druid wrote of any other value
		switch {
		case v.dt == timeseries.String:
			rb.SetTag(i, v.text)
		case d.dimRaw[i] == nil:
			rb.SetTag(i, jsonNull)
		default:
			rb.SetTag(i, d.dimRaw[i])
		}
		v.add(rb)
	}
	switch d.plan.QueryType() {
	case queryGroupBy:
		v := parseJSONValue(d.version, &d.scratch)
		d.typeOf(fieldVersion).add(v.dt)
		v.add(rb)
		rb.AddInt64(rank)
	case queryTopN:
		rb.AddInt64(rank)
	}
	for i, name := range d.values {
		v := parseJSONValue(d.cells[i], &d.scratch)
		d.typeOf(name).add(v.dt)
		v.add(rb)
	}
	return rb.Commit()
}

// addValue adds a value field a row names that no earlier one did
func (d *nativeDecoder) addValue(name string) int {
	d.values = append(d.values, name)
	d.byName[name] = len(d.values) - 1
	d.cells = append(d.cells, nil)
	d.b.AddValueField(timeseries.FieldDefinition{Name: name, Role: timeseries.RoleValue})
	return len(d.values) - 1
}

func (d *nativeDecoder) finish() (timeseries.Timeseries, error) {
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	// the values a row added follow the declared ones by name
	declared := len(d.plan.ValueFields())
	names := slices.Clone(d.values)
	slices.Sort(names[declared:])
	var order []int
	if !slices.Equal(names, d.values) {
		lead := len(d.dims) + d.extra()
		order = make([]int, lead+len(names))
		for i := range lead {
			order[i] = i
		}
		for i, name := range names {
			order[lead+i] = lead + d.byName[name]
		}
	}
	types := make(map[string]timeseries.FieldDataType, len(d.types)+1)
	for name, t := range d.types {
		types[name] = t.dt
	}
	types[fieldRank] = timeseries.Int64
	r := ds.Results[0]
	r.Name, r.StatementID = d.plan.QueryType(), 0
	for _, s := range r.SeriesList {
		if order != nil {
			s.ReorderValues(order)
		}
		s.Header = buildSeriesHeader(d.plan, d.trq, s.Header.Tags, names, types)
	}
	slices.SortFunc(r.SeriesList, func(a, b *dataset.Series) int {
		return stringsCompare(a.Header.Tags.JSON(), b.Header.Tags.JSON())
	})
	return &dataset.DataSet{Status: dataSetStatusSuccess, Results: dataset.Results{r}, TimeRangeQuery: d.trq}, nil
}
