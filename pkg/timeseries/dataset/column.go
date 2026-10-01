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
	"encoding/json"
	"math"
	"slices"
	"unsafe"
)

// Kind is the type of a value in a Column.
type Kind uint8

const (
	// KindNull is a missing value.
	KindNull Kind = iota
	// KindBool is a bool.
	KindBool
	// KindInt64 is an int64.
	KindInt64
	// KindUint64 is a uint64.
	KindUint64
	// KindFloat64 is a float64.
	KindFloat64
	// KindString is text, which Value returns as a string.
	KindString
	// KindBytes is opaque bytes, which Value returns as a []byte.
	KindBytes
	// KindNumber is a number's literal text, which Value returns as a json.Number.
	KindNumber
	// KindExt is any other value, held boxed beside the column's cells.
	KindExt
	// KindMixed is the kind of a column whose values differ in kind; each value then has its own.
	KindMixed
)

// IsBytes reports whether values of kind k are held as bytes: KindString, KindBytes and KindNumber.
func (k Kind) IsBytes() bool {
	return k == KindString || k == KindBytes || k == KindNumber
}

const (
	cellOffsetShift = 32
	cellLengthMask  = 1<<cellOffsetShift - 1
	// the most bytes a column's data can hold, as a cell's offset and length are 32 bits each
	maxColumnData = 1<<cellOffsetShift - 1
	// the memory estimate for each boxed KindExt value
	extValueSize = 32
)

// Column holds one value field's values for a Segment's rows, in 8-byte cells: a value, or a bytes
// value's offset and length into data. Only ext holds pointers, so its length costs the GC nothing.
type Column struct {
	// the kind of every value, or KindMixed when they differ; with tags, null values are allowed too
	kind Kind
	vals []uint64
	// each value's kind, only when some value is null or the kinds differ
	tags []Kind
	data []byte
	ext  []any
}

// Len returns the number of values in the Column.
func (c *Column) Len() int {
	return len(c.vals)
}

// Kind returns the kind of the Column's non-null values: KindMixed when they differ, and KindNull
// when there are none.
func (c *Column) Kind() Kind {
	return c.kind
}

// HasNulls reports whether any value in the Column may be null.
func (c *Column) HasNulls() bool {
	if c.tags == nil {
		return c.kind == KindNull && len(c.vals) > 0
	}
	return slices.Contains(c.tags, KindNull)
}

// KindAt returns the kind of value i.
func (c *Column) KindAt(i int) Kind {
	if c.tags != nil {
		return c.tags[i]
	}
	return c.kind
}

// IsNull reports whether value i is null.
func (c *Column) IsNull(i int) bool {
	return c.KindAt(i) == KindNull
}

// Float64 returns value i, which must be a KindFloat64.
func (c *Column) Float64(i int) float64 {
	return math.Float64frombits(c.vals[i])
}

// Int64 returns value i, which must be a KindInt64.
func (c *Column) Int64(i int) int64 {
	return int64(c.vals[i]) // #nosec G115 -- the cell holds an int64's bits
}

// Uint64 returns value i, which must be a KindUint64.
func (c *Column) Uint64(i int) uint64 {
	return c.vals[i]
}

// Bool returns value i, which must be a KindBool.
func (c *Column) Bool(i int) bool {
	return c.vals[i] != 0
}

// Bytes returns value i, which must be a KindString, KindBytes or KindNumber. The bytes are the
// Column's own and must not be modified.
func (c *Column) Bytes(i int) []byte {
	v := c.vals[i]
	off, n := v>>cellOffsetShift, v&cellLengthMask
	if n == 0 {
		return emptyBytes
	}
	return c.data[off : off+n : off+n]
}

// the bytes of every empty bytes value, which has no capacity to append into
var emptyBytes = []byte{}

// Text returns value i, which must be a KindString, KindBytes or KindNumber, as a string that shares
// the Column's memory.
func (c *Column) Text(i int) string {
	b := c.Bytes(i)
	if len(b) == 0 {
		return ""
	}
	// #nosec G103 -- the Column's data is never modified once built
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// Ext returns value i, which must be a KindExt.
func (c *Column) Ext(i int) any {
	return c.ext[c.vals[i]]
}

// Value returns value i boxed as its Go type: nil, bool, int64, uint64, float64, string, []byte,
// json.Number or the KindExt value. Boxing may allocate, so hot paths read by kind instead.
func (c *Column) Value(i int) any {
	switch c.KindAt(i) {
	case KindBool:
		return c.Bool(i)
	case KindInt64:
		return c.Int64(i)
	case KindUint64:
		return c.Uint64(i)
	case KindFloat64:
		return c.Float64(i)
	case KindString:
		return c.Text(i)
	case KindBytes:
		return c.Bytes(i)
	case KindNumber:
		return json.Number(c.Text(i))
	case KindExt:
		return c.Ext(i)
	}
	return nil
}

// Float64s returns the Column's values as a slice sharing its memory, when every one is a
// KindFloat64. The slice must not be modified.
func (c *Column) Float64s() ([]float64, bool) {
	if c.kind != KindFloat64 || c.tags != nil {
		return nil, false
	}
	if len(c.vals) == 0 {
		return []float64{}, true
	}
	// #nosec G103 -- a float64 and its uint64 bits have the same size and alignment
	return unsafe.Slice((*float64)(unsafe.Pointer(unsafe.SliceData(c.vals))), len(c.vals)), true
}

// Int64s returns the Column's values as a slice sharing its memory, when every one is a KindInt64.
// The slice must not be modified.
func (c *Column) Int64s() ([]int64, bool) {
	if c.kind != KindInt64 || c.tags != nil {
		return nil, false
	}
	if len(c.vals) == 0 {
		return []int64{}, true
	}
	// #nosec G103 -- an int64 and a uint64 have the same size and alignment
	return unsafe.Slice((*int64)(unsafe.Pointer(unsafe.SliceData(c.vals))), len(c.vals)), true
}

// Size returns the Column's memory in bytes: its cells, kinds and data, and an estimate for boxed
// values. A view counts all the data it keeps alive.
func (c *Column) Size() int64 {
	return int64(8*len(c.vals)+len(c.tags)+len(c.data)) + int64(len(c.ext))*extValueSize
}

// slice returns rows [from, to) of the Column, sharing its memory
func (c *Column) slice(from, to int) Column {
	out := Column{kind: c.kind, vals: c.vals[from:to:to], data: c.data, ext: c.ext}
	if c.tags != nil {
		out.tags = c.tags[from:to:to]
	}
	return out
}

// cellOf returns the kind and cell of a Go value; bytes kinds return their bytes for the caller to
// store, and KindExt returns v for the caller to box
func cellOf(v any) (Kind, uint64, []byte) {
	switch t := v.(type) {
	case nil:
		return KindNull, 0, nil
	case bool:
		if t {
			return KindBool, 1, nil
		}
		return KindBool, 0, nil
	case float64:
		return KindFloat64, math.Float64bits(t), nil
	case int64:
		return KindInt64, uint64(t), nil // #nosec G115 -- the cell holds the int64's bits
	case int:
		return KindInt64, uint64(t), nil // #nosec G115 -- as above
	case int32:
		return KindInt64, uint64(t), nil // #nosec G115 -- as above
	case int16:
		return KindInt64, uint64(t), nil // #nosec G115 -- as above
	case int8:
		return KindInt64, uint64(t), nil // #nosec G115 -- as above
	case uint64:
		return KindUint64, t, nil
	case uint32:
		return KindUint64, uint64(t), nil
	case uint16:
		return KindUint64, uint64(t), nil
	case uint8:
		return KindUint64, uint64(t), nil
	case string:
		// #nosec G103 -- the caller copies the bytes before the string can change
		return KindString, 0, unsafe.Slice(unsafe.StringData(t), len(t))
	case []byte:
		if t == nil {
			return KindNull, 0, nil
		}
		return KindBytes, 0, t
	case *[]byte:
		if t == nil {
			return KindNull, 0, nil
		}
		return KindBytes, 0, *t
	case *int64:
		if t == nil {
			return KindNull, 0, nil
		}
		return KindInt64, uint64(*t), nil // #nosec G115 -- as above
	case json.Number:
		// #nosec G103 -- as for string
		return KindNumber, 0, unsafe.Slice(unsafe.StringData(string(t)), len(t))
	}
	return KindExt, 0, nil
}

// kindTracker follows the kinds appended to a column, to decide its kind and whether it needs tags
type kindTracker struct {
	first Kind
	mixed bool
	nulls bool
}

func (t *kindTracker) add(k Kind) {
	switch {
	case k == KindNull:
		t.nulls = true
	case t.first == KindNull:
		t.first = k
	case k != t.first:
		t.mixed = true
	}
}

// the column's kind and whether each value needs its own tag
func (t *kindTracker) result() (Kind, bool) {
	switch {
	case t.mixed:
		return KindMixed, true
	case t.first == KindNull:
		return KindNull, false
	}
	return t.first, t.nulls
}
