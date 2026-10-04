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

package dataset

import (
	"fmt"
	"math"
	"strconv"

	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// AppendJSON appends column c's value at row i as tstrings.AppendJSONValue writes the value Value
// returns, reading the common kinds without boxing them.
func (s *Segment) AppendJSON(dst []byte, c, i int) ([]byte, error) {
	col := &s.cols[c]
	i += s.from
	switch col.KindAt(i) {
	case KindNull:
		return append(dst, "null"...), nil
	case KindString:
		return tstrings.AppendJSON(dst, col.Text(i)), nil
	case KindBool:
		return strconv.AppendBool(dst, col.Bool(i)), nil
	case KindInt64:
		return strconv.AppendInt(dst, col.Int64(i), 10), nil
	case KindUint64:
		return strconv.AppendUint(dst, col.Uint64(i), 10), nil
	case KindFloat64:
		if out, ok := tstrings.AppendJSONFloat(dst, col.Float64(i), 64); ok {
			return out, nil
		}
	}
	return tstrings.AppendJSONValue(dst, col.Value(i))
}

// CheckJSON reports the error AppendJSON would return for column c's value at row i, without writing it.
func (s *Segment) CheckJSON(c, i int) error {
	col := &s.cols[c]
	i += s.from
	switch col.KindAt(i) {
	case KindNull, KindString, KindBool, KindInt64, KindUint64:
		return nil
	case KindFloat64:
		if f := col.Float64(i); !math.IsNaN(f) && !math.IsInf(f, 0) {
			return nil
		}
	}
	return tstrings.CheckJSONValue(col.Value(i))
}

// CheckColumnJSON reports the first error AppendJSON would return for any of column c's values; kinds
// that always encode are skipped, and a float column is scanned without boxing.
func (s *Segment) CheckColumnJSON(c int) error {
	if col := &s.cols[c]; col.tags == nil {
		switch col.kind {
		case KindNull, KindString, KindBool, KindInt64, KindUint64:
			return nil
		case KindFloat64:
			for _, v := range col.vals[s.from : s.from+len(s.epochs)] {
				if f := math.Float64frombits(v); math.IsNaN(f) || math.IsInf(f, 0) {
					return tstrings.CheckJSONValue(f)
				}
			}
			return nil
		}
	}
	for i := range s.epochs {
		if err := s.CheckJSON(c, i); err != nil {
			return err
		}
	}
	return nil
}

// AppendFormatted appends a number, bool or null cell as fmt's %v writes it; for other kinds it appends
// nothing and reports false, and FormatText returns the text for the caller to quote.
func (s *Segment) AppendFormatted(dst []byte, c, i int) ([]byte, bool) {
	col := &s.cols[c]
	i += s.from
	switch col.KindAt(i) {
	case KindFloat64:
		return strconv.AppendFloat(dst, col.Float64(i), 'g', -1, 64), true
	case KindInt64:
		return strconv.AppendInt(dst, col.Int64(i), 10), true
	case KindUint64:
		return strconv.AppendUint(dst, col.Uint64(i), 10), true
	case KindBool:
		return strconv.AppendBool(dst, col.Bool(i)), true
	case KindNull:
		return append(dst, "<nil>"...), true
	}
	return dst, false
}

// FormatText returns column c's value at row i as fmt's %v writes it; text is returned as it is,
// sharing the column's memory.
func (s *Segment) FormatText(c, i int) string {
	col := &s.cols[c]
	if i += s.from; col.KindAt(i) == KindString {
		return col.Text(i)
	}
	return fmt.Sprint(col.Value(i))
}
