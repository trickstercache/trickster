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

import "strings"

// The scanners in this file read raw JSON values that a jsontext.Decoder has already validated, as
// ReadValue returns them, so they only find the boundaries of what the grammar guarantees is there.

// Elements iterates the elements of a JSON array value.
type Elements struct {
	raw []byte
	i   int
}

// ArrayElements returns an iterator over the elements of raw, a validated JSON value; a value that
// isn't an array has none.
func ArrayElements(raw []byte) Elements {
	if len(raw) == 0 || raw[0] != '[' {
		return Elements{}
	}
	return Elements{raw: raw, i: skipSpace(raw, 1)}
}

// Next returns the next element, or false after the last.
func (e *Elements) Next() ([]byte, bool) {
	if e.i >= len(e.raw) || e.raw[e.i] == ']' {
		return nil, false
	}
	end := valueEnd(e.raw, e.i)
	v := e.raw[e.i:end]
	e.i = next(e.raw, end)
	return v, true
}

// Members iterates the members of a JSON object value.
type Members struct {
	raw []byte
	i   int
}

// ObjectMembers returns an iterator over the members of raw, a validated JSON value; a value that
// isn't an object has none.
func ObjectMembers(raw []byte) Members {
	if len(raw) == 0 || raw[0] != '{' {
		return Members{}
	}
	return Members{raw: raw, i: skipSpace(raw, 1)}
}

// Next returns the next member's name, as its raw JSON string (see StringText), and value, or false
// after the last.
func (m *Members) Next() (name, value []byte, ok bool) {
	if m.i >= len(m.raw) || m.raw[m.i] != '"' {
		return nil, nil, false
	}
	end := stringEnd(m.raw, m.i)
	name = m.raw[m.i:end]
	// past the colon that separates the name from its value
	start := skipSpace(m.raw, skipSpace(m.raw, end)+1)
	end = valueEnd(m.raw, start)
	value = m.raw[start:end]
	m.i = next(m.raw, end)
	return name, value, true
}

// StringText returns the text of raw, a validated JSON string value: raw's own bytes when nothing in
// it is escaped, or else its text decoded into *buf, which is grown as needed.
func StringText(raw []byte, buf *[]byte) []byte {
	if n := len(raw); n >= 2 && plainASCII(raw[1:n-1]) {
		return raw[1 : n-1]
	}
	*buf = AppendString((*buf)[:0], raw)
	return *buf
}

// IsIntegerLiteral reports whether raw, a validated JSON number, has no fraction or exponent.
func IsIntegerLiteral(raw []byte) bool {
	for _, c := range raw {
		switch c {
		case '.', 'e', 'E':
			return false
		}
	}
	return true
}

// FieldName returns the one of names that key matches as encoding/json matches a struct field:
// exactly, or else ignoring case. It returns "" for none.
func FieldName(key []byte, names ...string) string {
	for _, n := range names {
		if string(key) == n {
			return n
		}
	}
	for _, n := range names {
		if strings.EqualFold(string(key), n) {
			return n
		}
	}
	return ""
}

// next returns the index of the value after the one ending at end, past any separating comma
func next(raw []byte, end int) int {
	i := skipSpace(raw, end)
	if i < len(raw) && raw[i] == ',' {
		i = skipSpace(raw, i+1)
	}
	return i
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
		if literalEnds[raw[j]] {
			return j
		}
	}
	return len(raw)
}

// the bytes that end a number, true, false or null
var literalEnds = [256]bool{',': true, ']': true, '}': true, ':': true, ' ': true, '\t': true, '\n': true, '\r': true}

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
