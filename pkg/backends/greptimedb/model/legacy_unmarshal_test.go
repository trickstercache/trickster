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
	"math"
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type legacySQLDecoder struct {
	trq           *timeseries.TimeRangeQuery
	plan          *sqlanalyzer.QueryPlan
	fields        timeseries.FieldDefinitions
	orderedFloats []bool
	builder       *dataset.Builder
	rowCount      uint64
	tagErr        error
}

// newLegacyDecoder is the decoder that boxed every value, kept as an oracle
func newLegacyDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrInvalidBody
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok || plan == nil || trq.Step <= 0 {
		return nil, timeseries.ErrInvalidBody
	}
	d := &legacySQLDecoder{trq: trq, plan: plan}
	return stream.NewJSON(d.walk, func() (timeseries.Timeseries, error) {
		ds, err := d.builder.Finish()
		if err != nil {
			return nil, err
		}
		return &dataSet{DataSet: ds, fields: d.fields}, nil
	}), nil
}

func (d *legacySQLDecoder) walk(dec *jsontext.Decoder) error {
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

func (d *legacySQLDecoder) readRecords(dec *jsontext.Decoder) error {
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

func (d *legacySQLDecoder) setSchema(columns []column) error {
	fields, err := fieldDefinitions(columns, d.plan)
	if err != nil {
		return err
	}
	d.fields = fields
	d.orderedFloats = make([]bool, len(fields))
	var sf timeseries.SeriesFields
	for i, field := range fields {
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

func (d *legacySQLDecoder) tagString(field timeseries.FieldDefinition, raw []byte) string {
	value, err := legacyDecodeRawValue(raw, field.SDataType)
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

func (d *legacySQLDecoder) readRows(dec *jsontext.Decoder) error {
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
			value, err := legacyDecodeRawValue(raw, field.SDataType)
			if err != nil {
				return err
			}
			if field.Role == timeseries.RoleTimestamp {
				ep, err := parseEpoch(json.Number(raw), field)
				if err != nil || !onGrid(ep, d.trq) {
					return timeseries.ErrInvalidTimeFormat
				}
				row.SetEpoch(epoch.Epoch(ep))
				return nil
			}
			row.AddValue(value)
			return nil
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

func legacyDecodeRawValue(raw []byte, typ string) (any, error) {
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
	return legacyDecodeValue(json.Number(raw), typ)
}

func legacyDecodeValue(value any, typ string) (any, error) {
	if value == nil {
		return nil, nil
	}
	kind, ok := fieldType(typ)
	if !ok {
		return nil, timeseries.ErrInvalidBody
	}
	switch kind {
	case timeseries.String:
		if v, ok := value.(string); ok {
			return v, nil
		}
	case timeseries.Bool:
		if v, ok := value.(bool); ok {
			return v, nil
		}
	case timeseries.Int64, timeseries.Uint64, timeseries.Float64:
		n, ok := value.(json.Number)
		if !ok {
			return nil, timeseries.ErrInvalidBody
		}
		bits := 64
		for _, width := range []int{8, 16, 32} {
			if typ == "Int"+strconv.Itoa(width) || typ == "UInt"+strconv.Itoa(width) || typ == "Float"+strconv.Itoa(width) {
				bits = width
			}
		}
		switch kind {
		case timeseries.Int64:
			return strconv.ParseInt(string(n), 10, bits)
		case timeseries.Uint64:
			return strconv.ParseUint(string(n), 10, bits)
		case timeseries.Float64:
			// JSON carries a decimal rendering of a Float32; do not widen a
			// rounded binary32 and change that decimal during reconstruction.
			v, err := strconv.ParseFloat(string(n), 64)
			if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && (bits == 64 || math.Abs(v) <= math.MaxFloat32) {
				return v, nil
			}
		}
	}
	return nil, timeseries.ErrInvalidBody
}
