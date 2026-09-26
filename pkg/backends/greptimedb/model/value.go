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
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func fieldType(name string) (timeseries.FieldDataType, bool) {
	switch name {
	case "Int8", "Int16", "Int32", "Int64", "TimestampSecond", "TimestampMillisecond", "TimestampMicrosecond", "TimestampNanosecond", "Date":
		return timeseries.Int64, true
	case "UInt8", "UInt16", "UInt32", "UInt64":
		return timeseries.Uint64, true
	case "Float32", "Float64":
		return timeseries.Float64, true
	case "String":
		return timeseries.String, true
	case "Boolean":
		return timeseries.Bool, true
	case "Null":
		return timeseries.Null, true
	}
	return timeseries.Unknown, false
}

func decodeValue(value any, typ string) (any, error) {
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

func axisScale(field timeseries.FieldDefinition) int64 {
	switch field.SDataType {
	case "TimestampSecond":
		return 1e9
	case "TimestampMillisecond":
		return 1e6
	case "TimestampMicrosecond":
		return 1e3
	case "TimestampNanosecond":
		return 1
	}
	if !strings.HasPrefix(field.SDataType, "Int") && !strings.HasPrefix(field.SDataType, "UInt") &&
		field.SDataType != "Float64" && field.SDataType != "Float32" {
		return 0
	}
	switch timeseries.FieldDataType(field.ProviderData1) {
	case timeseries.DateTimeUnixSecs:
		return 1e9
	case timeseries.DateTimeUnixMilli:
		return 1e6
	case timeseries.DateTimeUnixMicro:
		return 1e3
	case timeseries.DateTimeUnixNano:
		return 1
	}
	return 0
}

func parseEpoch(value any, field timeseries.FieldDefinition) (int64, error) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	scale := axisScale(field)
	if scale == 0 {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	if field.DataType == timeseries.Float64 {
		r, ok := new(big.Rat).SetString(string(n))
		if !ok {
			return 0, timeseries.ErrInvalidTimeFormat
		}
		r.Mul(r, new(big.Rat).SetInt64(scale))
		if !r.IsInt() || !r.Num().IsInt64() {
			return 0, timeseries.ErrInvalidTimeFormat
		}
		return r.Num().Int64(), nil
	}
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || v > math.MaxInt64/scale || v < math.MinInt64/scale {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	return v * scale, nil
}

func epochValue(ep int64, field timeseries.FieldDefinition) (any, error) {
	scale := axisScale(field)
	if scale == 0 {
		return nil, timeseries.ErrInvalidTimeFormat
	}
	if field.DataType == timeseries.Float64 {
		r := new(big.Rat).SetFrac(big.NewInt(ep), big.NewInt(scale))
		return json.Number(r.FloatString(9)), nil
	}
	if ep%scale != 0 {
		return nil, timeseries.ErrInvalidTimeFormat
	}
	value := ep / scale
	if field.DataType == timeseries.Uint64 {
		if value < 0 {
			return nil, timeseries.ErrInvalidTimeFormat
		}
		return uint64(value), nil
	}
	return value, nil
}
