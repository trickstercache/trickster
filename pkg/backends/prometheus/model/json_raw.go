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

// The functions in this file read raw JSON values that a jsontext.Decoder has already validated, so
// they only find the boundaries of what the grammar guarantees is there.

// the most digits an integer literal may have and be exact as a float64 written back the same way
const maxExactDigits = 15

// pairOf returns the first two elements of raw, a JSON array, and how many elements it has; a
// value that isn't an array has none
func pairOf(raw []byte) (first, second []byte, n int) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, nil, 0
	}
	i := skipSpace(raw, 1)
	for i < len(raw) && raw[i] != ']' {
		end := valueEnd(raw, i)
		switch n++; n {
		case 1:
			first = raw[i:end]
		case 2:
			second = raw[i:end]
		}
		i = skipSpace(raw, end)
		if i < len(raw) && raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	return first, second, n
}

// arrayLen returns the number of elements in raw, a JSON array, or 0 for any other value
func arrayLen(raw []byte) int {
	_, _, n := pairOf(raw)
	return n
}

func skipSpace(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// valueEnd returns the index just past the JSON value that starts at raw[i]
func valueEnd(raw []byte, i int) int {
	switch raw[i] {
	case '"':
		return stringEnd(raw, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(raw); j++ {
			switch raw[j] {
			case '"':
				j = stringEnd(raw, j) - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1
				}
			}
		}
		return len(raw)
	}
	for j := i; j < len(raw); j++ {
		switch raw[j] {
		case ',', ']', '}', ':', ' ', '\t', '\n', '\r':
			return j
		}
	}
	return len(raw)
}

// stringEnd returns the index just past the JSON string that starts at raw[i]
func stringEnd(raw []byte, i int) int {
	for j := i + 1; j < len(raw); j++ {
		switch raw[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(raw)
}

type member struct {
	name       []byte
	start, end int
}

// marshaler re-encodes raw JSON values, reusing its buffers; a nested object's names and members
// follow its parent's and are dropped when it's written
type marshaler struct {
	names   []byte
	members []member
}

// append appends raw as encoding/json's Marshal writes what Unmarshal decodes it into as an any;
// like Unmarshal, it fails for a number past a float64's range
func (m *marshaler) append(dst, raw []byte) ([]byte, error) {
	i := skipSpace(raw, 0)
	if i == len(raw) {
		return dst, timeseries.ErrInvalidBody
	}
	end := valueEnd(raw, i)
	v := raw[i:end]
	switch v[0] {
	case '{':
		return m.appendObject(dst, v)
	case '[':
		dst = append(dst, '[')
		for j := skipSpace(v, 1); j < len(v) && v[j] != ']'; {
			if dst[len(dst)-1] != '[' {
				dst = append(dst, ',')
			}
			e := valueEnd(v, j)
			var err error
			if dst, err = m.append(dst, v[j:e]); err != nil {
				return dst, err
			}
			if j = skipSpace(v, e); j < len(v) && v[j] == ',' {
				j = skipSpace(v, j+1)
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
	for j := skipSpace(v, 1); j < len(v) && v[j] != '}'; {
		ne := stringEnd(v, j)
		start := len(m.names)
		m.names = stream.AppendString(m.names, v[j:ne])
		vs := skipSpace(v, skipSpace(v, ne)+1)
		ve := valueEnd(v, vs)
		m.members = append(m.members, member{name: m.names[start:len(m.names):len(m.names)], start: vs, end: ve})
		if j = skipSpace(v, ve); j < len(v) && v[j] == ',' {
			j = skipSpace(v, j+1)
		}
	}
	members := m.members[memberMark:]
	// a stable sort keeps a repeated name's members in order, so its last is the one kept
	slices.SortStableFunc(members, func(a, b member) int { return bytes.Compare(a.name, b.name) })
	dst = append(dst, '{')
	for k, mb := range members {
		if k+1 < len(members) && bytes.Equal(members[k+1].name, mb.name) {
			continue
		}
		if dst[len(dst)-1] != '{' {
			dst = append(dst, ',')
		}
		dst = tstrings.AppendJSON(dst, string(mb.name))
		dst = append(dst, ':')
		var err error
		if dst, err = m.append(dst, v[mb.start:mb.end]); err != nil {
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
