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

package stream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// ErrInvalidValue indicates a value that cannot be parsed as its field's data type.
var ErrInvalidValue = fmt.Errorf("%w: invalid value", timeseries.ErrInvalidBody)

var nullLiteral = []byte("null")

// ParseValue parses the text of a value, such as a TSV or CSV cell, as dt. Text
// types return a string; other types return nil for empty text.
func ParseValue(raw []byte, dt timeseries.FieldDataType) (any, error) {
	switch dt {
	case timeseries.String, timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano,
		timeseries.DateSQL, timeseries.TimeSQL, timeseries.DateTimeSQL:
		return string(raw), nil
	case timeseries.Null:
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	var err error
	switch dt {
	case timeseries.Int64, timeseries.DateTimeUnixSecs, timeseries.DateTimeUnixMilli,
		timeseries.DateTimeUnixMicro, timeseries.DateTimeUnixNano:
		v, err = strconv.ParseInt(string(raw), 10, 64)
	case timeseries.Int16:
		v, err = strconv.ParseInt(string(raw), 10, 16)
	case timeseries.Byte:
		v, err = strconv.ParseInt(string(raw), 10, 8)
	case timeseries.Uint64:
		v, err = strconv.ParseUint(string(raw), 10, 64)
	case timeseries.Float64:
		v, err = strconv.ParseFloat(string(raw), 64)
	case timeseries.Bool:
		v, err = strconv.ParseBool(string(raw))
	case timeseries.Unknown:
		return inferValue(raw), nil
	default:
		return nil, ErrInvalidValue
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	return v, nil
}

// ParseJSONValue parses a raw JSON value as dt: null is nil, and quoted values
// are unquoted before parsing, since some APIs send numbers as JSON strings.
func ParseJSONValue(raw []byte, dt timeseries.FieldDataType) (any, error) {
	if bytes.Equal(raw, nullLiteral) {
		return nil, nil
	}
	if !isQuoted(raw) {
		return ParseValue(raw, dt)
	}
	b, err := unquote(raw)
	if err != nil {
		return nil, err
	}
	if dt == timeseries.Unknown {
		return string(b), nil
	}
	return ParseValue(b, dt)
}

func isQuoted(raw []byte) bool {
	n := len(raw)
	return n >= 2 && raw[0] == '"' && raw[n-1] == '"'
}

func unquote(raw []byte) ([]byte, error) {
	inner := raw[1 : len(raw)-1]
	if bytes.IndexByte(inner, '\\') < 0 {
		return inner, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	return []byte(s), nil
}

func inferValue(raw []byte) any {
	// JSON literals become bools and numbers; any other text stays a string
	switch string(raw) {
	case "true":
		return true
	case "false":
		return false
	}
	isInt, ok := jsonNumber(raw)
	if ok && isInt {
		if i, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(string(raw), 10, 64); err == nil {
			return u
		}
	}
	if ok {
		if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
			return f
		}
	}
	return string(raw)
}

func jsonNumber(b []byte) (isInt, ok bool) {
	// ok reports a match of the JSON number grammar; isInt, the absence of a fraction and exponent
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	start := i
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i == start || (b[start] == '0' && i-start > 1) {
		return false, false
	}
	isInt = true
	if i < len(b) && b[i] == '.' {
		isInt = false
		i++
		start = i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == start {
			return false, false
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		isInt = false
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		start = i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == start {
			return false, false
		}
	}
	return isInt, i == len(b)
}
