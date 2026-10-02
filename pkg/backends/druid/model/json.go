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
	"slices"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Druid writes JSON with Jackson, and these write it the same way

const upperHex = "0123456789ABCDEF"

// the JSON of the literals
const (
	jsonNullText = "null"
	jsonTrue     = "true"
	jsonFalse    = "false"
)

// a Java HashMap's default capacity, and its load factor as a fraction
const (
	javaMapCapacity = 16
	javaLoadNum     = 3
	javaLoadDen     = 4
)

// appendJacksonString appends s as Jackson quotes it: escaping quotes, backslashes, control characters
// and, as UTF-16 surrogates in uppercase hex, characters past the BMP
func appendJacksonString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r < 0x10000 {
				i += size
				continue
			}
			dst = append(dst, s[start:i]...)
			var units [2]uint16
			for _, u := range utf16.AppendRune(units[:0], r) {
				dst = appendJacksonEscape(dst, u)
			}
			i += size
			start = i
			continue
		}
		if c >= 0x20 && c != '"' && c != '\\' {
			i++
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\r':
			dst = append(dst, '\\', 'r')
		default:
			dst = appendJacksonEscape(dst, uint16(c))
		}
		i++
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// appendJacksonEscape appends a UTF-16 code unit's \u escape in uppercase hex
func appendJacksonEscape(dst []byte, u uint16) []byte {
	return append(dst, '\\', 'u', upperHex[u>>12], upperHex[u>>8&0xf], upperHex[u>>4&0xf], upperHex[u&0xf])
}

// appendDruidValue appends column c's value at row i as Druid wrote it: a string escaped as Jackson
// escapes it, and a number's literal or an object's or array's JSON as it was read
func appendDruidValue(dst []byte, seg *dataset.Segment, c, i int) ([]byte, error) {
	switch seg.KindAt(c, i) {
	case dataset.KindString:
		return appendJacksonString(dst, seg.Text(c, i)), nil
	case dataset.KindNumber, dataset.KindBytes:
		return append(dst, seg.Bytes(c, i)...), nil
	}
	return seg.AppendJSON(dst, c, i)
}

// checkDruidValue reports the error appendDruidValue would return for column c's value at row i; a
// number's literal and a compound value's JSON were read as valid JSON, so neither fails
func checkDruidValue(seg *dataset.Segment, c, i int) error {
	switch seg.KindAt(c, i) {
	case dataset.KindNumber, dataset.KindBytes:
		return nil
	}
	return seg.CheckJSON(c, i)
}

// checkDruidColumn reports the first error appendDruidValue would return for any of column c's values
func checkDruidColumn(seg *dataset.Segment, c int) error {
	// a column's kind is its non-null values', and nulls always encode
	col := seg.Col(c)
	switch col.Kind() {
	case dataset.KindNumber, dataset.KindBytes:
		return nil
	case dataset.KindMixed:
	default:
		return seg.CheckColumnJSON(c)
	}
	for i := range seg.Len() {
		if err := checkDruidValue(seg, c, i); err != nil {
			return err
		}
	}
	return nil
}

// timeText holds the last time written, which the rows of an epoch share
type timeText struct {
	e    epoch.Epoch
	text []byte
}

func (t *timeText) append(b []byte, e epoch.Epoch) []byte {
	if t.text == nil || t.e != e {
		t.text, t.e = appendTimestamp(t.text[:0], e), e
	}
	return append(b, t.text...)
}

// javaMapBucket returns the bucket of a HashMap table of capacity slots that holds key: Java's
// String.hashCode over its UTF-16 code units, spread as HashMap spreads it
func javaMapBucket(key string, capacity int) int {
	var h uint32
	var units [2]uint16
	for _, r := range key {
		for _, u := range utf16.AppendRune(units[:0], r) {
			h = 31*h + uint32(u)
		}
	}
	return int(h^h>>16) & (capacity - 1)
}

// javaMapCapacityFor returns the capacity of a HashMap copied from a map of n entries: the power of
// two at least n / 0.75 + 1, as float32 arithmetic gives it
func javaMapCapacityFor(n int) int {
	if n == 0 {
		return javaMapCapacity
	}
	target := int(float32(n)/0.75 + 1)
	capacity := 1
	for capacity < target {
		capacity <<= 1
	}
	return capacity
}

// javaMapOrder orders cells, in the order a Java HashMap of the given capacity was given their keys,
// as it iterates them: by bucket of its table, grown as its entries require, then as given
func javaMapOrder(cells []druidCell, capacity int) {
	for len(cells)*javaLoadDen > capacity*javaLoadNum {
		capacity <<= 1
	}
	buckets := make([]int, len(cells))
	for i := range cells {
		cells[i].given = i
		buckets[i] = javaMapBucket(cells[i].name, capacity)
	}
	slices.SortStableFunc(cells, func(a, b druidCell) int {
		return buckets[a.given] - buckets[b.given]
	})
}
