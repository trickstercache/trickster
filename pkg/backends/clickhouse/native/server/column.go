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
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
)

func encodeColumn(w io.Writer, typ string, values []any) error {
	col, err := column.Type(typ).Column("", &column.ServerContext{Revision: ServerRevision, Timezone: time.UTC})
	if err != nil {
		return err
	}
	for _, value := range values {
		value, err = columnValue(value, col.ScanType())
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

func columnValue(value any, target reflect.Type) (any, error) {
	if value == nil {
		return nil, nil
	}
	if reflect.TypeOf(value).AssignableTo(target) {
		return value, nil
	}
	if target == reflect.TypeFor[time.Time]() {
		if t, ok := temporalValue(value); ok {
			return t, nil
		}
		return nil, fmt.Errorf("invalid ClickHouse timestamp %q", value)
	}
	if target.Kind() == reflect.Pointer {
		inner, err := columnValue(value, target.Elem())
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
			v, err := columnValue(src.Index(i).Interface(), target.Elem())
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
			key, err := columnValue(iter.Key().Interface(), target.Key())
			if err != nil {
				return nil, err
			}
			v, err := columnValue(iter.Value().Interface(), target.Elem())
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
		if n, ok := value.(json.Number); ok {
			return string(n), nil
		}
		return value, nil
	}
	return out.Interface(), nil
}
