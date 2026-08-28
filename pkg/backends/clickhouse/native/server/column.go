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

package server

import (
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
)

func encodeColumnRevision(w io.Writer, typ string, values []any, revision uint64) error {
	col, err := column.Type(typ).Column("", &column.ServerContext{Revision: revision, Timezone: time.UTC})
	if err != nil {
		return err
	}
	location, err := columnTimezone(typ)
	if err != nil {
		return err
	}
	for _, value := range values {
		value, err = normalizeColumnValue(value, typ)
		if err != nil {
			return err
		}
		value, err = columnValue(value, col.ScanType(), location)
		if err != nil {
			return err
		}
		if err := col.AppendRow(value); err != nil {
			return err
		}
	}
	var buffer proto.Buffer
	if custom, ok := col.(column.CustomSerialization); ok {
		if err := custom.WriteStatePrefix(&buffer); err != nil {
			return err
		}
	}
	col.Encode(&buffer)
	_, err = w.Write(buffer.Buf)
	return err
}

func columnValue(value any, target reflect.Type, location *time.Location) (any, error) {
	if value == nil {
		return nil, nil
	}
	assignable := reflect.TypeOf(value).AssignableTo(target)
	genericElements := (target.Kind() == reflect.Slice || target.Kind() == reflect.Map) &&
		target.Elem().Kind() == reflect.Interface
	if assignable && target.Kind() != reflect.Interface && !genericElements {
		return value, nil
	}
	if target == reflect.TypeFor[time.Time]() {
		if t, ok := temporalValue(value, location); ok {
			return t, nil
		}
		return nil, fmt.Errorf("invalid ClickHouse timestamp %q", value)
	}
	if target.Kind() == reflect.Interface {
		return genericColumnValue(value), nil
	}
	if target.Kind() == reflect.Pointer {
		inner, err := columnValue(value, target.Elem(), location)
		if err != nil {
			return nil, err
		}
		p := reflect.New(target.Elem())
		if inner != nil && reflect.TypeOf(inner).AssignableTo(target.Elem()) {
			p.Elem().Set(reflect.ValueOf(inner))
			return p.Interface(), nil
		}
		return value, nil
	}
	text := fmt.Sprint(value)
	out := reflect.New(target).Elem()
	switch target.Kind() {
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		n, err := strconv.ParseInt(text, 10, target.Bits())
		if err != nil {
			return nil, err
		}
		out.SetInt(n)
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint:
		n, err := strconv.ParseUint(text, 10, target.Bits())
		if err != nil {
			return nil, err
		}
		out.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(text, target.Bits())
		if err != nil {
			return nil, err
		}
		out.SetFloat(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, err
		}
		out.SetBool(b)
	case reflect.Slice:
		src := reflect.ValueOf(value)
		if src.Kind() != reflect.Slice {
			return value, nil
		}
		out = reflect.MakeSlice(target, src.Len(), src.Len())
		for i := range src.Len() {
			v, err := columnValue(src.Index(i).Interface(), target.Elem(), location)
			if err != nil {
				return nil, err
			}
			if v != nil {
				rv := reflect.ValueOf(v)
				if !rv.Type().AssignableTo(target.Elem()) {
					return nil, fmt.Errorf("cannot convert %T to %s", v, target.Elem())
				}
				out.Index(i).Set(rv)
			}
		}
	case reflect.Map:
		src := reflect.ValueOf(value)
		if src.Kind() != reflect.Map {
			return nil, fmt.Errorf("cannot convert %T to %s", value, target)
		}
		out = reflect.MakeMapWithSize(target, src.Len())
		iter := src.MapRange()
		for iter.Next() {
			key, err := columnValue(iter.Key().Interface(), target.Key(), location)
			if err != nil {
				return nil, err
			}
			v, err := columnValue(iter.Value().Interface(), target.Elem(), location)
			if err != nil {
				return nil, err
			}
			rk, rv := reflect.ValueOf(key), reflect.Zero(target.Elem())
			if v != nil {
				rv = reflect.ValueOf(v)
			}
			if !rk.Type().AssignableTo(target.Key()) || !rv.Type().AssignableTo(target.Elem()) {
				return nil, fmt.Errorf("cannot convert map entry to %s", target)
			}
			out.SetMapIndex(rk, rv)
		}
	default:
		converted := reflect.New(target)
		if unmarshaler, ok := reflect.TypeAssert[encoding.TextUnmarshaler](converted); ok {
			if err := unmarshaler.UnmarshalText([]byte(text)); err != nil {
				return nil, err
			}
			return converted.Elem().Interface(), nil
		}
		return value, nil
	}
	return out.Interface(), nil
}

func normalizeColumnValue(value any, typ string) (any, error) {
	typ = strings.TrimSpace(typ)
	for _, wrapper := range []string{"Nullable(", "LowCardinality("} {
		if strings.HasPrefix(typ, wrapper) && strings.HasSuffix(typ, ")") {
			return normalizeColumnValue(value, typ[len(wrapper):len(typ)-1])
		}
	}
	if value == nil {
		return nil, nil
	}
	if strings.HasPrefix(typ, "Array(") && strings.HasSuffix(typ, ")") {
		items, ok := value.([]any)
		if !ok {
			return value, nil
		}
		inner := typ[len("Array(") : len(typ)-1]
		out := make([]any, len(items))
		for i, item := range items {
			normalized, err := normalizeColumnValue(item, inner)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	}
	if strings.HasPrefix(typ, "Tuple(") && strings.HasSuffix(typ, ")") {
		items, ok := value.([]any)
		if !ok {
			return value, nil
		}
		types := splitColumnTypes(typ[len("Tuple(") : len(typ)-1])
		if len(items) != len(types) {
			return value, nil
		}
		out := make([]any, len(items))
		for i, item := range items {
			normalized, err := normalizeColumnValue(item, types[i])
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	}
	text := fmt.Sprint(value)
	switch typ {
	case "Int8", "Int16", "Int32", "Int64":
		bits, _ := strconv.Atoi(strings.TrimPrefix(typ, "Int"))
		return strconv.ParseInt(text, 10, bits)
	case "UInt8", "UInt16", "UInt32", "UInt64":
		bits, _ := strconv.Atoi(strings.TrimPrefix(typ, "UInt"))
		return strconv.ParseUint(text, 10, bits)
	case "Int128", "Int256", "UInt128", "UInt256":
		if n, ok := new(big.Int).SetString(text, 10); ok {
			return n, nil
		}
		return nil, fmt.Errorf("invalid %s value %q", typ, text)
	case "Float32":
		return strconv.ParseFloat(text, 32)
	case "Float64":
		return strconv.ParseFloat(text, 64)
	default:
		return value, nil
	}
}

func splitColumnTypes(input string) []string {
	var (
		types  []string
		start  int
		depth  int
		quoted bool
	)
	for i, r := range input {
		switch r {
		case '\'':
			quoted = !quoted
		case '(':
			if !quoted {
				depth++
			}
		case ')':
			if !quoted {
				depth--
			}
		case ',':
			if !quoted && depth == 0 {
				types = append(types, strings.TrimSpace(input[start:i]))
				start = i + 1
			}
		}
	}
	return append(types, strings.TrimSpace(input[start:]))
}

func genericColumnValue(value any) any {
	switch value := value.(type) {
	case json.Number:
		text := string(value)
		if strings.ContainsAny(text, ".eE") {
			if n, err := value.Float64(); err == nil {
				return n
			}
			return text
		}
		if strings.HasPrefix(text, "-") {
			if n, err := value.Int64(); err == nil {
				return n
			}
		} else if n, err := strconv.ParseUint(text, 10, 64); err == nil {
			return n
		}
		if n, ok := new(big.Int).SetString(text, 10); ok {
			return n
		}
		return text
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = genericColumnValue(value[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = genericColumnValue(item)
		}
		return out
	default:
		return value
	}
}

func columnTimezone(typ string) (*time.Location, error) {
	if !strings.Contains(typ, "DateTime") {
		return time.UTC, nil
	}
	end := strings.LastIndexByte(typ, '\'')
	if end < 0 {
		return time.UTC, nil
	}
	start := strings.LastIndexByte(typ[:end], '\'')
	if start < 0 {
		return time.UTC, nil
	}
	return time.LoadLocation(typ[start+1 : end])
}
