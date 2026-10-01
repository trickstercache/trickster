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

package influxql

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	keyResults     = "results"
	keyError       = "error"
	keyStatementID = "statement_id"
	keySeries      = "series"
	keyName        = "name"
	keyTags        = "tags"
	keyColumns     = "columns"
	keyValues      = "values"
	keyPartial     = "partial"

	timeAltColumnName = "_time"
	// a time the decoder always rejected, as it once marked an unparsable time with it
	invalidTime = -1
)

// the keys a series object may hold, each once
const (
	seenName uint8 = 1 << iota
	seenTags
	seenColumns
	seenValues
	seenPartial
)

// UnmarshalTimeseries converts a JSON blob into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	return stream.BytesUnmarshaler(newDecoder)(data, trq)
}

// UnmarshalTimeseriesReader converts a JSON blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

// decoder builds a DataSet from an InfluxQL response as it is read. Each values row is read raw and
// its values added by their JSON literals, copied, so nothing refers to the input once it is read.
type decoder struct {
	trq         *timeseries.TimeRangeQuery
	b           *dataset.Builder
	docErr      string
	resultsSeen bool
	results     int
	// each statement's error, set on its result when the build finishes
	resultErrs map[int]string
	s          seriesState
	cells      [][]byte
	scratch    []byte
	interned   stream.Interner
	// a result's series or a series' values, copied when they arrive before what they need
	pendingSeries, pendingValues []byte
	// the last series' value fields, which the next series shares when its fields are the same
	fields []timeseries.FieldDefinition
}

// seriesState is the state of one series object
type seriesState struct {
	name    string
	tags    dataset.Tags
	columns []string
	seen    uint8
	// the time column's position, and whether the series has been started in the Builder
	ts      int
	started bool
	rows    int
	pending bool
	fields  []timeseries.FieldDefinition
}

func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	d := &decoder{trq: trq, b: dataset.NewBuilder(trq, dataset.BuilderOptions{})}
	return stream.NewJSON(d.walk, d.finish), nil
}

func (d *decoder) walk(dec *jsontext.Decoder) error {
	var seenResults, seenError bool
	err := stream.ObjectBytes(dec, func(key []byte) error {
		switch stream.FieldName(key, keyResults, keyError) {
		case keyResults:
			if seenResults {
				return timeseries.ErrInvalidBody
			}
			seenResults = true
			return d.readResults(dec)
		case keyError:
			if seenError {
				return timeseries.ErrInvalidBody
			}
			seenError = true
			return readString(dec, &d.scratch, &d.docErr)
		}
		return stream.Skip(dec)
	})
	if errors.Is(err, stream.ErrNull) {
		// a null document has no results, which finish reports
		return nil
	}
	return err
}

func (d *decoder) readResults(dec *jsontext.Decoder) error {
	err := stream.Array(dec, func() error { return d.readResult(dec) })
	if errors.Is(err, stream.ErrNull) {
		return nil
	}
	d.resultsSeen = err == nil
	return err
}

func (d *decoder) readResult(dec *jsontext.Decoder) error {
	var id int
	var errText string
	var seenID, seenSeries, seenError, pending bool
	err := stream.ObjectBytes(dec, func(key []byte) error {
		switch stream.FieldName(key, keyStatementID, keySeries, keyError) {
		case keyStatementID:
			if seenID {
				return timeseries.ErrInvalidBody
			}
			seenID = true
			return readInt(dec, &id)
		case keySeries:
			if seenSeries {
				return timeseries.ErrInvalidBody
			}
			seenSeries = true
			if !seenID {
				// the series belong to a statement not yet known, so they wait for its ID
				raw, err := dec.ReadValue()
				if err != nil {
					return err
				}
				d.pendingSeries, pending = append(d.pendingSeries[:0], raw...), true
				return nil
			}
			d.b.SetResult(id, "")
			return d.readSeriesList(dec)
		case keyError:
			if seenError {
				return timeseries.ErrInvalidBody
			}
			seenError = true
			return readString(dec, &d.scratch, &errText)
		}
		return stream.Skip(dec)
	})
	if err != nil {
		return err
	}
	d.b.SetResult(id, "")
	d.results++
	if pending {
		if err := d.readSeriesList(stream.NewJSONDecoder(bytes.NewReader(d.pendingSeries))); err != nil {
			return err
		}
	}
	if errText != "" {
		if d.resultErrs == nil {
			d.resultErrs = make(map[int]string)
		}
		d.resultErrs[id] = errText
	}
	return nil
}

func (d *decoder) readSeriesList(dec *jsontext.Decoder) error {
	err := stream.Array(dec, func() error { return d.readSeries(dec) })
	if errors.Is(err, stream.ErrNull) {
		return nil
	}
	return err
}

func (d *decoder) readSeries(dec *jsontext.Decoder) error {
	s := &d.s
	*s = seriesState{columns: s.columns[:0], fields: s.fields[:0]}
	err := stream.ObjectBytes(dec, func(key []byte) error {
		var bit uint8
		switch stream.FieldName(key, keyName, keyTags, keyColumns, keyValues, keyPartial) {
		case keyName:
			bit = seenName
		case keyTags:
			bit = seenTags
		case keyColumns:
			bit = seenColumns
		case keyValues:
			bit = seenValues
		case keyPartial:
			bit = seenPartial
		default:
			return stream.Skip(dec)
		}
		// a series is started once its values arrive, so a name or tags after them come too late
		if s.seen&bit != 0 || (s.started && bit&(seenName|seenTags) != 0) {
			return timeseries.ErrInvalidBody
		}
		s.seen |= bit
		switch bit {
		case seenName:
			return d.readName(dec)
		case seenTags:
			return d.readTags(dec)
		case seenColumns:
			return d.readColumns(dec)
		case seenValues:
			return d.readValuesKey(dec)
		}
		return readPartial(dec)
	})
	if err != nil {
		return err
	}
	if s.pending {
		if err := d.checkColumns(); err != nil {
			return err
		}
		if err := d.readValues(stream.NewJSONDecoder(bytes.NewReader(d.pendingValues))); err != nil {
			return err
		}
	}
	if s.seen&seenValues == 0 {
		return timeseries.ErrInvalidBody
	}
	if !s.started {
		d.startSeries()
	}
	d.b.EndSeries()
	return nil
}

func (d *decoder) readName(dec *jsontext.Decoder) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw.Kind() {
	case jsontext.KindString:
		d.s.name = d.interned.String(stream.StringText(raw, &d.scratch), true)
	case jsontext.KindNull:
	default:
		return timeseries.ErrInvalidBody
	}
	return nil
}

func (d *decoder) readTags(dec *jsontext.Decoder) error {
	if dec.PeekKind() == jsontext.KindNull {
		_, err := dec.ReadToken()
		return err
	}
	tags := dataset.Tags{}
	err := stream.ObjectBytes(dec, func(key []byte) error {
		name := d.interned.String(key, true)
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		switch raw.Kind() {
		case jsontext.KindString:
			tags[name] = d.interned.Short(stream.StringText(raw, &d.scratch))
		case jsontext.KindNull:
			tags[name] = ""
		default:
			return timeseries.ErrInvalidBody
		}
		return nil
	})
	d.s.tags = tags
	return err
}

func (d *decoder) readColumns(dec *jsontext.Decoder) error {
	err := stream.Array(dec, func() error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		var name string
		switch raw.Kind() {
		case jsontext.KindString:
			name = d.interned.String(stream.StringText(raw, &d.scratch), true)
		case jsontext.KindNull:
		default:
			return timeseries.ErrInvalidBody
		}
		d.s.columns = append(d.s.columns, name)
		return nil
	})
	if errors.Is(err, stream.ErrNull) {
		return nil
	}
	return err
}

// checkColumns requires a time column and at least one other, and finds the time column
func (d *decoder) checkColumns() error {
	s := &d.s
	if len(s.columns) < 2 {
		return timeseries.ErrInvalidBody
	}
	s.ts = -1
	for i, c := range s.columns {
		if c == timeColumnName || c == timeAltColumnName {
			s.ts = i
		}
	}
	if s.ts < 0 {
		return timeseries.ErrInvalidBody
	}
	// every column but the time column holds a value; another time column's field is unnamed, last
	s.fields = s.fields[:0]
	for _, c := range s.columns {
		if c != timeColumnName && c != timeAltColumnName {
			s.fields = append(s.fields, timeseries.FieldDefinition{Name: c})
		}
	}
	for len(s.fields) < len(s.columns)-1 {
		s.fields = append(s.fields, timeseries.FieldDefinition{})
	}
	return nil
}

func (d *decoder) readValuesKey(dec *jsontext.Decoder) error {
	// null values, like none, fail the series
	if dec.PeekKind() != jsontext.KindBeginArray {
		return timeseries.ErrInvalidBody
	}
	if d.s.seen&seenColumns == 0 {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		d.pendingValues, d.s.pending = append(d.pendingValues[:0], raw...), true
		return nil
	}
	if err := d.checkColumns(); err != nil {
		return err
	}
	return d.readValues(dec)
}

func (d *decoder) readValues(dec *jsontext.Decoder) error {
	return stream.Array(dec, func() error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		return d.row(raw)
	})
}

// row adds one values row, whose first row types the series' fields; a row at epoch 0 is dropped
func (d *decoder) row(raw []byte) error {
	s := &d.s
	if raw[0] != '[' {
		return timeseries.ErrInvalidBody
	}
	d.cells = d.cells[:0]
	els := stream.ArrayElements(raw)
	for v, ok := els.Next(); ok; v, ok = els.Next() {
		d.cells = append(d.cells, v)
	}
	if len(d.cells) <= s.ts || len(d.cells)-1 > len(s.fields) {
		return timeseries.ErrInvalidBody
	}
	e, err := d.rowTime(d.cells[s.ts])
	if err != nil {
		return err
	}
	for _, c := range d.cells {
		if c[0] == '[' || c[0] == '{' {
			return timeseries.ErrInvalidTimeFormat
		}
	}
	if s.rows++; s.rows == 1 {
		if e != 0 {
			d.typeFields()
		}
		d.startSeries()
	}
	if e == 0 {
		return nil
	}
	r := d.b.Row()
	r.SetEpoch(e)
	for i, c := range d.cells {
		if i == s.ts {
			continue
		}
		if err := d.addValue(r, c); err != nil {
			return err
		}
	}
	// a short row's missing values are null
	for range len(s.fields) - (len(d.cells) - 1) {
		r.AddNull()
	}
	return r.Commit()
}

// typeFields sets each field's type from the value the first row holds for it
func (d *decoder) typeFields() {
	s := &d.s
	f := 0
	for i, c := range d.cells {
		if i == s.ts {
			continue
		}
		switch c[0] {
		case '"':
			s.fields[f].DataType = timeseries.String
		case 't', 'f':
			s.fields[f].DataType = timeseries.Bool
		case 'n':
		default:
			// every number was a float64 once, and its type stays so the series' identity does
			s.fields[f].DataType = timeseries.Float64
		}
		f++
	}
}

func (d *decoder) startSeries() {
	s := &d.s
	if !slices.Equal(s.fields, d.fields) {
		d.fields = slices.Clip(slices.Clone(s.fields))
	}
	d.b.StartSeries(dataset.SeriesHeader{
		Name:            s.name,
		Tags:            s.tags,
		TimestampField:  timeseries.FieldDefinition{Name: s.columns[s.ts], OutputPosition: s.ts},
		ValueFieldsList: d.fields,
		QueryStatement:  d.trq.Statement,
	})
	s.started = true
}

// rowTime parses a row's time: a count of nanoseconds, or an RFC 3339 string
func (d *decoder) rowTime(raw []byte) (epoch.Epoch, error) {
	var e epoch.Epoch
	switch c := raw[0]; {
	case c == '"':
		text := stream.StringText(raw, &d.scratch)
		var err error
		if e, err = epoch.ParseRFC3339(text, time.RFC3339); err != nil {
			if e, err = epoch.ParseRFC3339(text, time.RFC3339Nano); err != nil {
				return 0, timeseries.ErrInvalidTimeFormat
			}
		}
	case c == '-' || (c >= '0' && c <= '9'):
		var err error
		if e, err = epoch.ParseDecimal(raw, timeseries.DateTimeUnixNano); err != nil {
			// a fraction of a nanosecond, or a time past an int64, is truncated as a float's once was
			f, err := strconv.ParseFloat(string(raw), 64)
			if err != nil {
				return 0, timeseries.ErrInvalidBody
			}
			e = epoch.Epoch(f)
		}
	default:
		return 0, timeseries.ErrInvalidTimeFormat
	}
	if e == invalidTime {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	return e, nil
}

// addValue adds a value by its JSON literal: a number written without a fraction or exponent as an
// int64 when it fits one, and any other as a float64
func (d *decoder) addValue(r *dataset.RowBuilder, raw []byte) error {
	switch raw[0] {
	case '"':
		r.AddString(stream.StringText(raw, &d.scratch))
	case 't':
		r.AddBool(true)
	case 'f':
		r.AddBool(false)
	case 'n':
		r.AddNull()
	default:
		if stream.IsIntegerLiteral(raw) {
			if v, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
				if v == 0 && raw[0] == '-' {
					// -0 is a float's, which an int64 can't hold
					r.AddFloat64(math.Copysign(0, -1))
					return nil
				}
				r.AddInt64(v)
				return nil
			}
		}
		f, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return timeseries.ErrInvalidBody
		}
		r.AddFloat64(f)
	}
	return nil
}

func (d *decoder) finish() (timeseries.Timeseries, error) {
	if !d.resultsSeen {
		return nil, timeseries.ErrInvalidBody
	}
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	ds.Error = d.docErr
	if d.results == 0 {
		// the Builder always has a result, which an empty results array doesn't
		ds.Results = dataset.Results{}
	}
	for _, r := range ds.Results {
		r.Error = d.resultErrs[r.StatementID]
	}
	return ds, nil
}

func readString(dec *jsontext.Decoder, scratch *[]byte, out *string) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw.Kind() {
	case jsontext.KindString:
		*out = string(stream.StringText(raw, scratch))
	case jsontext.KindNull:
	default:
		return timeseries.ErrInvalidBody
	}
	return nil
}

func readInt(dec *jsontext.Decoder, out *int) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw.Kind() {
	case jsontext.KindNumber:
		v, err := strconv.Atoi(string(raw))
		if err != nil {
			return timeseries.ErrInvalidBody
		}
		*out = v
	case jsontext.KindNull:
	default:
		return timeseries.ErrInvalidBody
	}
	return nil
}

func readPartial(dec *jsontext.Decoder) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw.Kind() {
	case jsontext.KindTrue, jsontext.KindFalse, jsontext.KindNull:
		return nil
	}
	return timeseries.ErrInvalidBody
}
