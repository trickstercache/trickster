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
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type column struct {
	Name string `json:"name"`
	Type string `json:"data_type"`
}

type schema struct {
	Columns []column `json:"column_schemas"`
}

func UnmarshalTimeseries(body []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return stream.BytesUnmarshaler(newDecoder)(body, trq)
}

func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

type sqlDecoder struct {
	trq           *timeseries.TimeRangeQuery
	plan          *sqlanalyzer.QueryPlan
	fields        timeseries.FieldDefinitions
	orderedFloats []bool
	// per field: its type's width in bits, and a time's scale
	bits     []int
	scales   []int64
	builder  *dataset.Builder
	rowCount uint64
	tagErr   error
	// a string value's text, unquoted
	text []byte
}

func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrInvalidBody
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok || plan == nil || trq.Step <= 0 {
		return nil, timeseries.ErrInvalidBody
	}
	d := &sqlDecoder{trq: trq, plan: plan}
	return stream.NewJSON(d.walk, func() (timeseries.Timeseries, error) {
		ds, err := d.builder.Finish()
		if err != nil {
			return nil, err
		}
		return &dataSet{DataSet: ds, fields: d.fields}, nil
	}), nil
}

func (d *sqlDecoder) walk(dec *jsontext.Decoder) error {
	var hasOutput bool
	var elapsed *uint64
	err := stream.Object(dec, func(key string) error {
		switch key {
		case "output":
			if hasOutput {
				return timeseries.ErrInvalidBody
			}
			hasOutput = true
			count := 0
			err := stream.Array(dec, func() error {
				count++
				if count != 1 {
					return timeseries.ErrInvalidBody
				}
				return stream.Object(dec, func(key string) error {
					if key != "records" || d.builder != nil {
						return timeseries.ErrInvalidBody
					}
					return d.readRecords(dec)
				})
			})
			if err == nil && count != 1 {
				return timeseries.ErrInvalidBody
			}
			return err
		case "execution_time_ms":
			if elapsed != nil {
				return timeseries.ErrInvalidBody
			}
			return stream.Decode(dec, &elapsed)
		}
		return timeseries.ErrInvalidBody
	})
	if err == nil && (!hasOutput || elapsed == nil || d.builder == nil) {
		return timeseries.ErrInvalidBody
	}
	return err
}

func (d *sqlDecoder) readRecords(dec *jsontext.Decoder) error {
	var rowsSeen bool
	var total *uint64
	var pending []byte
	err := stream.Object(dec, func(key string) error {
		switch key {
		case "schema":
			if d.builder != nil {
				return timeseries.ErrInvalidBody
			}
			var schema schema
			if err := stream.Decode(dec, &schema, jsonv2.RejectUnknownMembers(true)); err != nil {
				return err
			}
			return d.setSchema(schema.Columns)
		case "rows":
			if rowsSeen {
				return timeseries.ErrInvalidBody
			}
			rowsSeen = true
			if d.builder == nil {
				// JSON object keys are unordered; only this alternate layout needs a buffer.
				raw, err := dec.ReadValue()
				pending = bytes.Clone(raw)
				return err
			}
			return d.readRows(dec)
		case "total_rows":
			if total != nil {
				return timeseries.ErrInvalidBody
			}
			return stream.Decode(dec, &total)
		case "metrics":
			err := stream.Object(dec, func(string) error { return timeseries.ErrInvalidBody })
			if errors.Is(err, stream.ErrNull) {
				return nil
			}
			return err
		}
		return timeseries.ErrInvalidBody
	})
	if err != nil {
		return err
	}
	if d.builder == nil || !rowsSeen || total == nil {
		return timeseries.ErrInvalidBody
	}
	if pending != nil {
		if err := d.readRows(stream.NewJSONDecoder(bytes.NewReader(pending))); err != nil {
			return err
		}
	}
	if *total != d.rowCount {
		return timeseries.ErrInvalidBody
	}
	return nil
}

func (d *sqlDecoder) setSchema(columns []column) error {
	fields, err := fieldDefinitions(columns, d.plan)
	if err != nil {
		return err
	}
	d.fields = fields
	d.orderedFloats = make([]bool, len(fields))
	d.bits, d.scales = make([]int, len(fields)), make([]int64, len(fields))
	var sf timeseries.SeriesFields
	for i, field := range fields {
		d.bits[i], d.scales[i] = typeBits(field.SDataType), axisScale(field)
		d.orderedFloats[i] = field.DataType == timeseries.Float64 && slices.ContainsFunc(d.plan.Ordering, func(term timeseries.OrderTerm) bool {
			return term.Column == field.Name
		})
		switch field.Role {
		case timeseries.RoleTimestamp:
			sf.Timestamp = field
		case timeseries.RoleTag:
			sf.Tags = append(sf.Tags, field)
		case timeseries.RoleValue:
			sf.Values = append(sf.Values, field)
		}
	}
	d.builder = dataset.NewBuilder(d.trq, dataset.BuilderOptions{
		Fields: sf, SeriesName: "sql", QueryStatement: d.trq.Statement,
		Duplicates: dataset.DuplicatesError, TagString: d.tagString,
	})
	return nil
}

func (d *sqlDecoder) tagString(field timeseries.FieldDefinition, raw []byte) string {
	if field.SDataType == "String" && marshalsAsItself(raw) {
		return string(raw)
	}
	value, err := decodeRawValue(raw, field.SDataType)
	if err != nil {
		d.tagErr = err
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		d.tagErr = err
	}
	return string(encoded)
}

func (d *sqlDecoder) readRows(dec *jsontext.Decoder) error {
	return stream.Array(dec, func() error {
		row := d.builder.Row()
		i, tag := 0, 0
		// each value is read raw, valid only until the next read, and copied by the row as needed
		err := stream.Array(dec, func() error {
			if i >= len(d.fields) {
				return timeseries.ErrInvalidBody
			}
			raw, err := dec.ReadValue()
			if err != nil {
				return err
			}
			field := d.fields[i]
			// JSON null cannot distinguish SQL NULL from NaN/Inf for numeric sorting.
			if d.orderedFloats[i] && bytes.Equal(raw, []byte("null")) {
				return timeseries.ErrInvalidBody
			}
			i++
			if field.Role == timeseries.RoleTag {
				row.SetTag(tag, raw)
				tag++
				return nil
			}
			if field.Role == timeseries.RoleTimestamp {
				ep, err := d.readEpoch(raw, i-1)
				if err != nil {
					return err
				}
				row.SetEpoch(epoch.Epoch(ep))
				return nil
			}
			return d.addValue(row, raw, i-1)
		})
		if err != nil {
			return err
		}
		if i != len(d.fields) {
			return timeseries.ErrInvalidBody
		}
		if err := row.Commit(); err != nil {
			return err
		}
		d.rowCount++
		return d.tagErr
	})
}

// readEpoch reads field i's time, failing as decodeRawValue would for its type, and then as parseEpoch would
func (d *sqlDecoder) readEpoch(raw []byte, i int) (int64, error) {
	field := &d.fields[i]
	if field.DataType != timeseries.Int64 || bytes.Equal(raw, jsonNull) {
		if _, err := decodeRawValue(raw, field.SDataType); err != nil {
			return 0, err
		}
		ep, err := parseEpoch(json.Number(raw), *field)
		if err != nil || !onGrid(ep, d.trq) {
			return 0, timeseries.ErrInvalidTimeFormat
		}
		return ep, nil
	}
	// an integer of the type's width is one of 64 bits
	v, err := strconv.ParseInt(string(raw), 10, d.bits[i])
	if err != nil {
		return 0, err
	}
	scale := d.scales[i]
	if scale == 0 || v > math.MaxInt64/scale || v < math.MinInt64/scale || !onGrid(v*scale, d.trq) {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	return v * scale, nil
}

// addValue adds field i's value with the typed adders, or as decodeRawValue decodes it when the value
// isn't of the field's type, so that it fails alike
func (d *sqlDecoder) addValue(row *dataset.RowBuilder, raw []byte, i int) error {
	if bytes.Equal(raw, jsonNull) {
		row.AddNull()
		return nil
	}
	switch d.fields[i].DataType {
	case timeseries.String:
		if raw[0] == '"' {
			d.text = stream.AppendString(d.text[:0], raw)
			row.AddString(d.text)
			return nil
		}
	case timeseries.Bool:
		switch raw[0] {
		case 't':
			row.AddBool(true)
			return nil
		case 'f':
			row.AddBool(false)
			return nil
		}
	case timeseries.Int64:
		v, err := strconv.ParseInt(string(raw), 10, d.bits[i])
		if err != nil {
			return err
		}
		row.AddInt64(v)
		return nil
	case timeseries.Uint64:
		v, err := strconv.ParseUint(string(raw), 10, d.bits[i])
		if err != nil {
			return err
		}
		row.AddUint64(v)
		return nil
	case timeseries.Float64:
		v, ok := parseFloat(string(raw), d.bits[i])
		if !ok {
			return timeseries.ErrInvalidBody
		}
		row.AddFloat64(v)
		return nil
	}
	value, err := decodeRawValue(raw, d.fields[i].SDataType)
	if err != nil {
		return err
	}
	row.AddValue(value)
	return nil
}

// marshalsAsItself reports whether raw is a JSON string that encoding/json writes back as raw: one without
// escapes, and without the HTML characters and line separators it escapes
func marshalsAsItself(raw []byte) bool {
	if !plainJSONString(raw) {
		return false
	}
	for i, c := range raw {
		switch {
		case c == '<', c == '>', c == '&':
			return false
		// U+2028 and U+2029
		case c == 0xe2 && i+2 < len(raw) && raw[i+1] == 0x80 && (raw[i+2] == 0xa8 || raw[i+2] == 0xa9):
			return false
		}
	}
	return true
}

func decodeRawValue(raw []byte, typ string) (any, error) {
	if bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	switch typ {
	case "String":
		var value string
		err := json.Unmarshal(raw, &value)
		return value, err
	case "Boolean":
		var value bool
		err := json.Unmarshal(raw, &value)
		return value, err
	}
	return decodeValue(json.Number(raw), typ)
}

func fieldDefinitions(columns []column, plan *sqlanalyzer.QueryPlan) (timeseries.FieldDefinitions, error) {
	roles := map[string]timeseries.FieldRole{plan.OutputColumn: timeseries.RoleTimestamp}
	for _, name := range plan.GroupColumns {
		roles[name] = timeseries.RoleTag
	}
	for _, name := range plan.ValueColumns {
		roles[name] = timeseries.RoleValue
	}
	if len(roles) > len(columns) || (len(plan.ValueColumns) > 0 && len(roles) != len(columns)) {
		return nil, timeseries.ErrInvalidBody
	}
	fields := make(timeseries.FieldDefinitions, len(columns))
	seen := make(map[string]bool, len(columns))
	values := 0
	for i, col := range columns {
		role, ok := roles[col.Name]
		// The Cockroach adapter currently names only timestamp/group fields.
		// Remaining columns are typed values supplied by the origin schema.
		if !ok && len(plan.ValueColumns) == 0 {
			role, ok = timeseries.RoleValue, true
		}
		typ, supported := fieldType(col.Type)
		if !ok || !supported || col.Name == "" || seen[col.Name] {
			return nil, timeseries.ErrInvalidBody
		}
		seen[col.Name] = true
		delete(roles, col.Name)
		field := timeseries.FieldDefinition{
			Name: col.Name, SDataType: col.Type,
			DataType: typ, Role: role, OutputPosition: i,
		}
		if role == timeseries.RoleTimestamp {
			field.ProviderData1 = byte(plan.OutputUnit)
			if axisScale(field) == 0 {
				return nil, timeseries.ErrInvalidTimeFormat
			}
		}
		fields[i] = field
		if role == timeseries.RoleValue {
			values++
		}
	}
	// The common dataset treats a series without values as empty when cropping.
	if len(roles) != 0 || values == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	for _, term := range plan.Ordering {
		if !seen[term.Column] {
			return nil, timeseries.ErrInvalidBody
		}
	}
	return fields, nil
}

func onGrid(value int64, trq *timeseries.TimeRangeQuery) bool {
	step := int64(trq.Step)
	r, phase := value%step, int64(trq.Phase)%step
	if r < 0 {
		r += step
	}
	if phase < 0 {
		phase += step
	}
	return r == phase
}
