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
	"bytes"
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// the most digits an integer literal may have and be exact as a float64 written back the same way
const maxExactDigits = 15

// pairOf returns the first two elements of raw, a JSON array a decoder has validated, and how many
// elements it has; a value that isn't an array has none
func pairOf(raw []byte) (first, second []byte, n int) {
	els := stream.ArrayElements(raw)
	for v, ok := els.Next(); ok; v, ok = els.Next() {
		switch n++; n {
		case 1:
			first = v
		case 2:
			second = v
		}
	}
	return first, second, n
}

// arrayLen returns the number of elements in raw, a JSON array, or 0 for any other value
func arrayLen(raw []byte) int {
	_, _, n := pairOf(raw)
	return n
}

type member struct {
	name, value []byte
}

// marshaler re-encodes raw JSON values, reusing its buffers; a nested object's names and members
// follow its parent's and are dropped when it's written
type marshaler struct {
	names   []byte
	members []member
}

// append appends v, a JSON value a decoder has validated, as encoding/json's Marshal writes what
// Unmarshal decodes it into as an any; like Unmarshal, it fails for a number past a float64's range
func (m *marshaler) append(dst, v []byte) ([]byte, error) {
	if len(v) == 0 {
		return dst, timeseries.ErrInvalidBody
	}
	switch v[0] {
	case '{':
		return m.appendObject(dst, v)
	case '[':
		dst = append(dst, '[')
		els := stream.ArrayElements(v)
		for e, ok := els.Next(); ok; e, ok = els.Next() {
			if dst[len(dst)-1] != '[' {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = m.append(dst, e); err != nil {
				return dst, err
			}
		}
		return append(dst, ']'), nil
	case '"':
		return appendString(dst, v), nil
	case 't', 'f', 'n':
		return append(dst, v...), nil
	}
	return appendNumber(dst, v)
}

func (m *marshaler) appendObject(dst, v []byte) ([]byte, error) {
	nameMark, memberMark := len(m.names), len(m.members)
	defer func() { m.names, m.members = m.names[:nameMark], m.members[:memberMark] }()
	members := stream.ObjectMembers(v)
	for name, value, ok := members.Next(); ok; name, value, ok = members.Next() {
		start := len(m.names)
		m.names = stream.AppendString(m.names, name)
		m.members = append(m.members, member{name: m.names[start:len(m.names):len(m.names)], value: value})
	}
	sorted := m.members[memberMark:]
	// a stable sort keeps a repeated name's members in order, so its last is the one kept
	slices.SortStableFunc(sorted, func(a, b member) int { return bytes.Compare(a.name, b.name) })
	dst = append(dst, '{')
	for k, mb := range sorted {
		if k+1 < len(sorted) && bytes.Equal(sorted[k+1].name, mb.name) {
			continue
		}
		if dst[len(dst)-1] != '{' {
			dst = append(dst, ',')
		}
		dst = tstrings.AppendJSON(dst, string(mb.name))
		dst = append(dst, ':')
		var err error
		if dst, err = m.append(dst, mb.value); err != nil {
			return dst, err
		}
	}
	return append(dst, '}'), nil
}

// appendString appends a JSON string re-escaped as encoding/json escapes it; text that needs no
// escaping, as sample text never does, is copied as it is
func appendString(dst, v []byte) []byte {
	if safeString(v[1 : len(v)-1]) {
		return append(dst, v...)
	}
	return tstrings.AppendJSON(dst, string(stream.AppendString(nil, v)))
}

// safeString reports whether a JSON string's text is written back as it is: ASCII that is neither
// escaped nor escaped by encoding/json, which escapes HTML's special characters
func safeString(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 || c < 0x20 || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

// appendNumber appends a JSON number as the float64 it decodes to; a short integer is written as it is
func appendNumber(dst, v []byte) ([]byte, error) {
	if exactInteger(v) {
		return append(dst, v...), nil
	}
	f, err := strconv.ParseFloat(string(v), 64)
	if err != nil {
		return dst, timeseries.ErrInvalidBody
	}
	out, ok := tstrings.AppendJSONFloat(dst, f, 64)
	if !ok {
		return dst, timeseries.ErrInvalidBody
	}
	return out, nil
}

// exactInteger reports whether v is an integer literal a float64 holds exactly and writes the same
func exactInteger(v []byte) bool {
	digits := v
	if len(digits) > 0 && digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 || len(digits) > maxExactDigits {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
