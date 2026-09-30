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

package sql

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	// the name of every series a v3 response decodes to
	seriesName = "default"
	// the first bytes of a JSON array body and a JSON Lines body; a CSV body starts with anything else
	openJSON  = '['
	openJSONL = '{'
	// joins a series' tag values into the key that orders the series
	tagKeySeparator = "\x00"
	// integer timestamps below each of these are seconds, milliseconds and microseconds
	secondsBelow      = 100_000_000_000
	millisecondsBelow = 100_000_000_000_000
	microsecondsBelow = 100_000_000_000_000_000
)

// v3TimestampLayouts are the string timestamp shapes InfluxDB 3 emits. The native v3 output is naive
// UTC without a zone suffix (2026-08-29T01:33:10); RFC3339 variants are accepted for robustness.
var v3TimestampLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

// UnmarshalTimeseries converts a v3 response body into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return stream.BytesUnmarshaler(newDecoder)(data, trq)
}

// UnmarshalTimeseriesReader converts a v3 response body into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

// column is one name of the first row: the time, and a tag or a value field, which slot holds
type column struct {
	name string
	slot int
	time bool
}

// decoder builds a DataSet from a v3 query response: a JSON array of row objects, JSON Lines of them,
// or CSV with a header record. The first row names the columns, and each value column takes its type
// from its first value that is neither null nor empty.
type decoder struct {
	trq    *timeseries.TimeRangeQuery
	b      *dataset.Builder
	tsName string
	// the first row's names in order, so a row in the same order finds each name without a lookup
	columns []column
	byName  map[string]int
	tags    []string
	fields  timeseries.FieldDefinitions
	typed   []bool
	// a row's tag and value cells by slot, tags first, and its time's cell
	cells    [][]byte
	timeCell []byte
	// a CSV record's column at each position
	positions []int
	scratch   []byte
	nameBuf   []byte
}

func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	d := &decoder{trq: trq}
	return stream.Sniff(d.pick), nil
}

func (d *decoder) pick(first byte) stream.Decoder {
	switch first {
	case openJSON:
		return stream.NewJSON(d.walkArray, d.finish)
	case openJSONL:
		return stream.NewJSON(d.walkLines, d.finish)
	}
	return stream.NewCSV(d.csvRecord, d.finish)
}

func (d *decoder) walkArray(dec *jsontext.Decoder) error {
	return stream.Array(dec, func() error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		return d.jsonRow(raw)
	})
}

// walkLines reads JSON Lines, whose rows are JSON values one after another
func (d *decoder) walkLines(dec *jsontext.Decoder) error {
	for {
		raw, err := dec.ReadValue()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := d.jsonRow(raw); err != nil {
			return err
		}
	}
}

// setColumns lays out the columns the first row names: the time, the query's tags, and the values
func (d *decoder) setColumns(names []string) {
	d.tsName = d.trq.TimestampDefinition.Name
	if d.tsName == "" {
		d.tsName = DefaultTimestampField
	}
	// tag columns partition rows into series; they come from the analyzed GROUP BY columns and
	// identify each series for delta merging
	for _, fd := range d.trq.TagFieldDefintions {
		if slices.Contains(names, fd.Name) && !slices.Contains(d.tags, fd.Name) {
			d.tags = append(d.tags, fd.Name)
		}
	}
	d.byName = make(map[string]int, len(names))
	for _, name := range names {
		if _, ok := d.byName[name]; ok {
			continue
		}
		c := column{name: name, slot: -1, time: name == d.tsName}
		if i := slices.Index(d.tags, name); i >= 0 {
			c.slot = i
		} else if !c.time {
			c.slot = len(d.tags) + len(d.fields)
			d.fields = append(d.fields, timeseries.FieldDefinition{
				Name: name, OutputPosition: len(d.fields), Role: timeseries.RoleValue,
			})
		}
		d.byName[name] = len(d.columns)
		d.columns = append(d.columns, c)
	}
	d.fields = slices.Clip(d.fields)
	d.typed = make([]bool, len(d.fields))
	d.cells = make([][]byte, len(d.tags)+len(d.fields))
	tagFields := make(timeseries.FieldDefinitions, len(d.tags))
	for i, name := range d.tags {
		tagFields[i] = timeseries.FieldDefinition{Name: name, Role: timeseries.RoleTag}
	}
	d.b = dataset.NewBuilder(d.trq, dataset.BuilderOptions{
		Fields: timeseries.SeriesFields{
			Timestamp: timeseries.FieldDefinition{
				Name: d.tsName, DataType: timeseries.DateTimeRFC3339Nano, Role: timeseries.RoleTimestamp,
			},
			Tags:   tagFields,
			Values: d.fields,
		},
		SeriesName:     seriesName,
		QueryStatement: d.trq.Statement,
	})
}

// jsonRow adds a JSON row object; a null row holds nothing, not even a time
func (d *decoder) jsonRow(raw []byte) error {
	switch raw[0] {
	case '{':
	case 'n':
		if d.b == nil {
			return timeseries.ErrInvalidBody
		}
		return nil
	default:
		return timeseries.ErrInvalidBody
	}
	if d.b == nil {
		d.setColumns(d.jsonNames(raw))
	}
	clear(d.cells)
	d.timeCell = nil
	members := stream.ObjectMembers(raw)
	next := 0
	for name, value, ok := members.Next(); ok; name, value, ok = members.Next() {
		key := stream.StringText(name, &d.nameBuf)
		i := -1
		if next < len(d.columns) && string(key) == d.columns[next].name {
			i = next
		} else if j, found := d.byName[string(key)]; found {
			i = j
		} else if string(key) == d.tsName {
			d.timeCell = value
			continue
		}
		if i < 0 {
			continue
		}
		next = i + 1
		// a repeated name's last value is the one kept
		c := d.columns[i]
		if c.slot >= 0 {
			d.cells[c.slot] = value
		}
		if c.time {
			d.timeCell = value
		}
	}
	nt := len(d.tags)
	for j := range d.fields {
		if !d.typed[j] {
			d.typeJSON(j, d.cells[nt+j])
		}
	}
	e, ok := d.jsonTime(d.timeCell)
	if !ok {
		// a row without a parseable timestamp can't be placed in time, but still types its values
		return nil
	}
	r := d.b.Row()
	r.SetEpoch(e)
	for i := range d.tags {
		d.setJSONTag(r, i, d.cells[i])
	}
	for j := range d.fields {
		d.addJSONValue(r, d.cells[nt+j], d.fields[j].DataType)
	}
	return r.Commit()
}

func (d *decoder) jsonNames(raw []byte) []string {
	var names []string
	members := stream.ObjectMembers(raw)
	for name, _, ok := members.Next(); ok; name, _, ok = members.Next() {
		names = append(names, string(stream.StringText(name, &d.nameBuf)))
	}
	return names
}

// typeJSON types value field j by a JSON value, unless it's null or an empty string
func (d *decoder) typeJSON(j int, raw []byte) {
	if raw == nil {
		return
	}
	var dt timeseries.FieldDataType
	switch raw[0] {
	case 'n':
		return
	case '"':
		text := stream.StringText(raw, &d.scratch)
		if len(text) == 0 {
			return
		}
		dt = sniffText(text)
	case 't', 'f':
		dt = timeseries.Bool
	case '{', '[':
		dt = timeseries.String
	default:
		dt = timeseries.Float64
		if stream.IsIntegerLiteral(raw) {
			dt = timeseries.Int64
		}
	}
	d.fields[j].DataType, d.typed[j] = dt, true
}

// sniffText types text, which every CSV value is, as the first of an integer, a float or a bool it parses as
func sniffText(text []byte) timeseries.FieldDataType {
	s := string(text)
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return timeseries.Int64
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return timeseries.Float64
	}
	if _, err := strconv.ParseBool(s); err == nil {
		return timeseries.Bool
	}
	return timeseries.String
}

// jsonTime parses a row's time: text as textTime does, or an integer by its magnitude
func (d *decoder) jsonTime(raw []byte) (epoch.Epoch, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	switch raw[0] {
	case '"':
		return textTime(stream.StringText(raw, &d.scratch))
	case 't', 'f', 'n', '{', '[':
		return 0, false
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return epochFromInteger(n), true
}

// textTime parses a time written in one of the v3 layouts, or as an integer
func textTime(text []byte) (epoch.Epoch, bool) {
	if e, ok := epoch.ParseCanonicalTime(text, false); ok {
		return e, true
	}
	if e, ok := epoch.ParseCanonicalTime(text, true); ok {
		return e, true
	}
	s := string(text)
	for _, layout := range v3TimestampLayouts {
		if ts, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return epoch.Epoch(ts.UnixNano()), true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return epochFromInteger(n), true
	}
	return 0, false
}

// epochFromInteger infers the epoch unit of an integer timestamp by magnitude:
// values below 1e11 are seconds, below 1e14 milliseconds, below 1e17
// microseconds, and nanoseconds beyond.
func epochFromInteger(value int64) epoch.Epoch {
	magnitude := value
	if magnitude < 0 {
		magnitude = -magnitude
	}
	switch {
	case magnitude < secondsBelow:
		return epoch.Epoch(value * int64(time.Second))
	case magnitude < millisecondsBelow:
		return epoch.Epoch(value * int64(time.Millisecond))
	case magnitude < microsecondsBelow:
		return epoch.Epoch(value * int64(time.Microsecond))
	default:
		return epoch.Epoch(value)
	}
}

// setJSONTag sets tag i to a JSON value's text: a string's, a literal's own, or "" for null
func (d *decoder) setJSONTag(r *dataset.RowBuilder, i int, raw []byte) {
	if len(raw) == 0 {
		r.SetTag(i, nil)
		return
	}
	switch raw[0] {
	case 'n':
		r.SetTag(i, nil)
	case '"':
		r.SetTag(i, stream.StringText(raw, &d.scratch))
	case '{', '[':
		r.SetTag(i, fmt.Append(d.scratch[:0], decodeAny(raw)))
	default:
		r.SetTag(i, raw)
	}
}

// addJSONValue adds a JSON value converted to the field's type where it converts, and otherwise as it is
func (d *decoder) addJSONValue(r *dataset.RowBuilder, raw []byte, dt timeseries.FieldDataType) {
	if len(raw) == 0 {
		r.AddNull()
		return
	}
	switch raw[0] {
	case 'n':
		r.AddNull()
	case '"':
		addText(r, stream.StringText(raw, &d.scratch), dt)
	case 't', 'f':
		if dt == timeseries.String {
			r.AddString(raw)
			return
		}
		r.AddBool(raw[0] == 't')
	case '{', '[':
		v := decodeAny(raw)
		if dt == timeseries.String {
			r.AddString(fmt.Append(d.scratch[:0], v))
			return
		}
		r.AddValue(v)
	default:
		addNumber(r, raw, dt)
	}
}

// addNumber adds a JSON number converted to the field's type, or as its literal where it doesn't convert
func addNumber(r *dataset.RowBuilder, raw []byte, dt timeseries.FieldDataType) {
	switch dt {
	case timeseries.Int64:
		if stream.IsIntegerLiteral(raw) {
			if n, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
				r.AddInt64(n)
				return
			}
		}
		fallthrough
	case timeseries.Float64:
		if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
			r.AddFloat64(f)
			return
		}
	case timeseries.String:
		r.AddString(raw)
		return
	}
	r.AddNumber(raw)
}

// addText adds text converted to the field's type where it converts, and otherwise as text
func addText(r *dataset.RowBuilder, text []byte, dt timeseries.FieldDataType) {
	switch dt {
	case timeseries.Int64:
		if isDecimal(text) {
			if n, err := strconv.ParseInt(string(text), 10, 64); err == nil {
				r.AddInt64(n)
				return
			}
		}
		fallthrough
	case timeseries.Float64:
		if f, err := strconv.ParseFloat(string(text), 64); err == nil {
			r.AddFloat64(f)
			return
		}
	case timeseries.Bool:
		if b, err := strconv.ParseBool(string(text)); err == nil {
			r.AddBool(b)
			return
		}
	}
	r.AddString(text)
}

// isDecimal reports whether text is a signed run of digits, which is all strconv.ParseInt accepts
func isDecimal(text []byte) bool {
	if len(text) > 0 && (text[0] == '-' || text[0] == '+') {
		text = text[1:]
	}
	if len(text) == 0 {
		return false
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// decodeAny decodes a JSON object or array as encoding/json does into an any, with its numbers as
// json.Numbers, which only nested values need
func decodeAny(raw []byte) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	// raw was validated by the decoder that read it
	_ = dec.Decode(&v)
	return v
}

// csvRecord takes the first record as the header, and each after it as a row
func (d *decoder) csvRecord(fields [][]byte) error {
	if d.b == nil {
		names := make([]string, len(fields))
		for i, f := range fields {
			names[i] = string(f)
		}
		d.setColumns(names)
		d.positions = make([]int, len(names))
		for i, name := range names {
			d.positions[i] = d.byName[name]
		}
		return nil
	}
	d.timeCell = nil
	for i, f := range fields {
		// a repeated name's last value is the one kept
		c := d.columns[d.positions[i]]
		if c.slot >= 0 {
			d.cells[c.slot] = f
		}
		if c.time {
			d.timeCell = f
		}
	}
	nt := len(d.tags)
	for j := range d.fields {
		if text := d.cells[nt+j]; !d.typed[j] && len(text) > 0 {
			d.fields[j].DataType, d.typed[j] = sniffText(text), true
		}
	}
	if d.timeCell == nil {
		return nil
	}
	e, ok := textTime(d.timeCell)
	if !ok {
		return nil
	}
	r := d.b.Row()
	r.SetEpoch(e)
	for i := range d.tags {
		r.SetTag(i, d.cells[i])
	}
	for j := range d.fields {
		addText(r, d.cells[nt+j], d.fields[j].DataType)
	}
	return r.Commit()
}

func (d *decoder) finish() (timeseries.Timeseries, error) {
	if d.b == nil {
		return &dataset.DataSet{
			TimeRangeQuery: d.trq,
			ExtentList:     timeseries.ExtentList{d.trq.Extent},
			Results:        dataset.Results{&dataset.Result{SeriesList: dataset.SeriesList{}}},
		}, nil
	}
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	// a column no value typed is text; every series shares the fields, now that they're typed
	for j := range d.fields {
		if !d.typed[j] {
			d.fields[j].DataType = timeseries.String
		}
	}
	type keyed struct {
		key string
		s   *dataset.Series
	}
	sl := ds.Results[0].SeriesList
	byKey := make([]keyed, len(sl))
	parts := make([]string, len(d.tags))
	for i, s := range sl {
		for t, name := range d.tags {
			parts[t] = s.Header.Tags[name]
		}
		byKey[i] = keyed{key: strings.Join(parts, tagKeySeparator), s: s}
		s.Header.ValueFieldsList = d.fields
		s.Header.CalculateSize()
	}
	// series are ordered by their tag values, in the order of the query's tags
	slices.SortFunc(byKey, func(a, b keyed) int { return cmp.Compare(a.key, b.key) })
	for i := range byKey {
		sl[i] = byKey[i].s
	}
	return ds, nil
}
