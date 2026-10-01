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
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// colKind is how a nativeColumn holds its values
type colKind uint8

const (
	// text holds each row's value as its TSV text
	colText colKind = iota
	// words hold each row's value
	colInt
	colUint
	colFloat64
	colFloat32
	colBool
	colDateTime
	colDateTime64
	colDate
	colDate32
	// words hold each row's index into the dictionary
	colDict
)

const (
	// the type wrappers and compound types whose values aren't one fixed value per row
	prefixNullable       = "Nullable("
	prefixLowCardinality = "LowCardinality("
	prefixArray          = "Array("
	prefixMap            = "Map("
	prefixTuple          = "Tuple("
	prefixDateTime       = "DateTime("
	prefixDateTime64     = "DateTime64"
	prefixDecimal        = "Decimal"
	prefixFixedString    = "FixedString("
	typeDate32           = "Date32"
	typeUUID             = "UUID"
	typeIPv4             = "IPv4"
	typeIPv6             = "IPv6"
	// LowCardinality's serialization: the key version its state prefix declares, and the flags that
	// precede each block's dictionary
	lowCardinalityVersion     = 1
	lowCardinalityWidthMask   = 0xff
	lowCardinalityGlobalDict  = 1 << 8
	lowCardinalityAdditional  = 1 << 9
	lowCardinalityMaxKeyWidth = 8
	offsetSize                = 8
	dateLayout                = "2006-01-02"
	dateTimeLayout            = "2006-01-02 15:04:05"
)

// the types that are read as neither text nor values, as their values aren't one per row
var rejectedTypes = []string{
	"Nested(", "Variant(", "Dynamic", "JSON", "Object(", "AggregateFunction(", "SimpleAggregateFunction(",
}

var (
	nullText = []byte(nullToken)
	// the NULL of an element of a compound value
	nullLiteral = []byte("NULL")
)

// nativeColumn holds one block's values of a column: the fixed-width values of the common types,
// and the TSV text of the others, which a value is typed from as its TSV text would be
type nativeColumn struct {
	kind colKind
	rows int
	// nonzero for a row that's NULL
	nulls []byte
	words []uint64
	// the rows' texts, back to back, and where each ends
	data []byte
	ends []int
	// a compound type's elements' texts, while they're written
	scratch []byte
	// DateTime64's digits of a second, and the layout of its text
	precision int
	layout    string
	// LowCardinality's dictionary, which words index
	dict *nativeColumn
	// a compound type's element columns
	elems []*nativeColumn
}

// readColumn reads a column's serialization state prefix (LowCardinality members declare their
// dictionary version there, even when nested) and then its values
func (c *nativeColumn) readColumn(cur *nativeCursor, typ string, n int) error {
	if err := readStatePrefix(cur, strings.TrimSpace(typ)); err != nil {
		return err
	}
	return c.readValues(cur, typ, n)
}

func readStatePrefix(cur *nativeCursor, typ string) error {
	switch {
	case strings.HasPrefix(typ, prefixLowCardinality):
		raw, err := cur.fixed(offsetSize)
		if err != nil {
			return err
		}
		if v := binary.LittleEndian.Uint64(raw); v != lowCardinalityVersion {
			return fmt.Errorf("%w: LowCardinality key version %d", errUnsupportedSerialization, v)
		}
	case strings.HasPrefix(typ, prefixNullable):
		return readStatePrefix(cur, innerType(typ, prefixNullable))
	case strings.HasPrefix(typ, prefixArray):
		return readStatePrefix(cur, innerType(typ, prefixArray))
	case strings.HasPrefix(typ, prefixMap), strings.HasPrefix(typ, prefixTuple):
		open := strings.IndexByte(typ, '(')
		for _, t := range splitTypeList(typ[open+1 : max(open+1, len(typ)-1)]) {
			if err := readStatePrefix(cur, elementType(t)); err != nil {
				return err
			}
		}
	}
	return nil
}

// innerType returns the type that a wrapper's prefix and closing parenthesis enclose
func innerType(typ, prefix string) string {
	return typ[len(prefix):max(len(prefix), len(typ)-1)]
}

// elementType returns a tuple element's type without the name a named tuple gives it
func elementType(t string) string {
	if sp := strings.IndexByte(t, ' '); sp > 0 && !strings.ContainsAny(t[:sp], "()'") {
		return strings.TrimSpace(t[sp+1:])
	}
	return t
}

func cutWrapper(typ, prefix string) (string, bool) {
	if strings.HasPrefix(typ, prefix) && strings.HasSuffix(typ, ")") {
		return typ[len(prefix) : len(typ)-1], true
	}
	return "", false
}

// readValues reads n values of typ, after a Nullable's null map; encodings that aren't one value per
// row (Nested, Variant, Dynamic, JSON) are rejected rather than misread.
func (c *nativeColumn) readValues(cur *nativeCursor, typ string, n int) error {
	typ = strings.TrimSpace(typ)
	// every type takes at least a byte a row, so more rows than bytes can't have arrived yet
	if n > cur.remaining() {
		return errShort
	}
	if inner, ok := cutWrapper(typ, prefixNullable); ok {
		nulls, err := cur.fixed(n)
		if err != nil {
			return err
		}
		if err := c.readValues(cur, inner, n); err != nil {
			return err
		}
		c.setNulls(nulls)
		return nil
	}
	c.kind, c.rows, c.nulls = colText, n, c.nulls[:0]
	c.data, c.ends = c.data[:0], c.ends[:0]
	if inner, ok := cutWrapper(typ, prefixLowCardinality); ok {
		return c.readLowCardinality(cur, inner, n)
	}
	if inner, ok := cutWrapper(typ, prefixArray); ok {
		return c.readArray(cur, inner, n)
	}
	if inner, ok := cutWrapper(typ, prefixMap); ok {
		return c.readMap(cur, inner, n)
	}
	if inner, ok := cutWrapper(typ, prefixTuple); ok {
		return c.readTuple(cur, inner, n)
	}
	for _, compound := range rejectedTypes {
		if strings.HasPrefix(typ, compound) {
			return fmt.Errorf("%w: %s", errUnsupportedColumnType, typ)
		}
	}
	return c.readScalars(cur, typ, n)
}

// setNulls marks the rows a null map holds as NULL, as well as any the values held
func (c *nativeColumn) setNulls(nulls []byte) {
	if len(c.nulls) == 0 {
		c.nulls = append(c.nulls, nulls...)
		return
	}
	for i, isNull := range nulls {
		c.nulls[i] |= isNull
	}
}

func (c *nativeColumn) readScalars(cur *nativeCursor, typ string, n int) error {
	switch typ {
	case TypeUInt8:
		return c.readWords(cur, n, 1, colUint, false)
	case TypeBool:
		return c.readWords(cur, n, 1, colBool, false)
	case TypeUInt16:
		return c.readWords(cur, n, 2, colUint, false)
	case TypeUInt32:
		return c.readWords(cur, n, 4, colUint, false)
	case TypeUInt64:
		return c.readWords(cur, n, 8, colUint, false)
	case TypeInt8:
		return c.readWords(cur, n, 1, colInt, true)
	case TypeInt16:
		return c.readWords(cur, n, 2, colInt, true)
	case TypeInt32:
		return c.readWords(cur, n, 4, colInt, true)
	case TypeInt64:
		return c.readWords(cur, n, 8, colInt, true)
	case TypeFloat32:
		return c.readWords(cur, n, 4, colFloat32, false)
	case TypeFloat64:
		return c.readWords(cur, n, 8, colFloat64, false)
	case TypeDateTime:
		return c.readWords(cur, n, 4, colDateTime, false)
	case TypeDate:
		return c.readWords(cur, n, 2, colDate, false)
	case TypeString:
		return c.readStrings(cur, n)
	case typeDate32:
		return c.readWords(cur, n, 4, colDate32, true)
	case "Int128", "UInt128":
		return c.readBigInts(cur, n, 16, typ[0] == 'I')
	case "Int256", "UInt256":
		return c.readBigInts(cur, n, 32, typ[0] == 'I')
	case typeUUID:
		return c.readEach(cur, n, 16, appendUUID)
	case typeIPv4:
		return c.readEach(cur, n, 4, func(dst, b []byte) []byte {
			return append(dst, net.IPv4(b[3], b[2], b[1], b[0]).String()...)
		})
	case typeIPv6:
		return c.readEach(cur, n, 16, func(dst, b []byte) []byte { return append(dst, net.IP(b).String()...) })
	}
	switch {
	case strings.HasPrefix(typ, prefixDateTime64):
		text := strings.TrimSpace(strings.Split(strings.TrimSuffix(strings.TrimPrefix(typ,
			prefixDateTime64+"("), ")"), ",")[0])
		precision, err := strconv.Atoi(text)
		if err != nil || precision < 0 || precision > maxPrecision {
			return fmt.Errorf("invalid DateTime64 precision %q", text)
		}
		c.precision, c.layout = precision, dateTimeLayout
		if precision > 0 {
			c.layout += "." + strings.Repeat("0", precision)
		}
		return c.readWords(cur, n, 8, colDateTime64, false)
	case strings.HasPrefix(typ, prefixDateTime):
		return c.readWords(cur, n, 4, colDateTime, false)
	case strings.HasPrefix(typ, "Enum8("), strings.HasPrefix(typ, "Enum16("):
		return c.readEnums(cur, typ, n)
	case strings.HasPrefix(typ, prefixDecimal):
		return c.readDecimals(cur, typ, n)
	}
	if after, ok := strings.CutPrefix(typ, prefixFixedString); ok {
		size, err := strconv.Atoi(strings.TrimSuffix(after, ")"))
		if err != nil || size < 0 {
			return fmt.Errorf("invalid FixedString length in %q", typ)
		}
		return c.readEach(cur, n, size, func(dst, b []byte) []byte {
			end := len(b)
			for end > 0 && b[end-1] == 0 {
				end--
			}
			return append(dst, b[:end]...)
		})
	}
	// any other type is read as a string
	return c.readStrings(cur, n)
}

// readWords reads n little-endian values of size bytes each, sign-extending those that are signed
func (c *nativeColumn) readWords(cur *nativeCursor, n, size int, kind colKind, signed bool) error {
	raw, err := cur.words(n, size)
	if err != nil {
		return err
	}
	c.kind = kind
	c.words = growTo(c.words, n)
	w := c.words
	switch size {
	case 1:
		for i, b := range raw {
			if signed {
				w[i] = uint64(int64(int8(b))) //nolint:gosec // sign extension
			} else {
				w[i] = uint64(b)
			}
		}
	case 2:
		for i := range n {
			v := binary.LittleEndian.Uint16(raw[2*i:])
			if signed {
				w[i] = uint64(int64(int16(v))) //nolint:gosec // sign extension
			} else {
				w[i] = uint64(v)
			}
		}
	case 4:
		for i := range n {
			v := binary.LittleEndian.Uint32(raw[4*i:])
			if signed {
				w[i] = uint64(int64(int32(v))) //nolint:gosec // sign extension
			} else {
				w[i] = uint64(v)
			}
		}
	default:
		for i := range n {
			w[i] = binary.LittleEndian.Uint64(raw[8*i:])
		}
	}
	return nil
}

func growTo[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

func (c *nativeColumn) readStrings(cur *nativeCursor, n int) error {
	c.ends = growTo(c.ends, n)[:0]
	for i := range n {
		s, err := cur.str()
		if err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		c.data = append(c.data, s...)
		c.ends = append(c.ends, len(c.data))
	}
	return nil
}

// readEach reads n values of size bytes each as the text appendText writes for them
func (c *nativeColumn) readEach(cur *nativeCursor, n, size int, appendText func(dst, b []byte) []byte) error {
	c.ends = growTo(c.ends, n)[:0]
	for i := range n {
		b, err := cur.fixed(size)
		if err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		c.data = appendText(c.data, b)
		c.ends = append(c.ends, len(c.data))
	}
	return nil
}

// appendUUID appends a UUID, which ClickHouse stores as two little-endian UInt64 halves
func appendUUID(dst, b []byte) []byte {
	var u [16]byte
	for i := range 8 {
		u[i] = b[7-i]
		u[8+i] = b[15-i]
	}
	dst = hex.AppendEncode(dst, u[0:4])
	dst = hex.AppendEncode(append(dst, '-'), u[4:6])
	dst = hex.AppendEncode(append(dst, '-'), u[6:8])
	dst = hex.AppendEncode(append(dst, '-'), u[8:10])
	return hex.AppendEncode(append(dst, '-'), u[10:16])
}

func (c *nativeColumn) readBigInts(cur *nativeCursor, n, size int, signed bool) error {
	return c.readEach(cur, n, size, func(dst, b []byte) []byte { return appendBigInt(dst, b, signed) })
}

// appendBigInt appends a little-endian two's-complement integer
func appendBigInt(dst, b []byte, signed bool) []byte {
	if signed && len(b) <= 8 {
		var v uint64
		for _, c := range slices.Backward(b) {
			v = v<<8 | uint64(c)
		}
		shift := 64 - 8*len(b)
		return strconv.AppendInt(dst, int64(v<<shift)>>shift, 10) //nolint:gosec // sign extension
	}
	be := make([]byte, len(b))
	for i := range b {
		be[i] = b[len(b)-1-i]
	}
	v := new(big.Int).SetBytes(be)
	if signed && be[0]&0x80 != 0 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), uint(8*len(b)))) //nolint:gosec // 16 or 32 bytes
	}
	return v.Append(dst, 10)
}

// readEnums reads Enum8 or Enum16 indexes as their member names, or as the bare index when the
// declaration doesn't list it
func (c *nativeColumn) readEnums(cur *nativeCursor, typ string, n int) error {
	members := parseEnumMembers(typ)
	size := 2
	if strings.HasPrefix(typ, "Enum8(") {
		size = 1
	}
	return c.readEach(cur, n, size, func(dst, b []byte) []byte {
		index := int(int8(b[0])) //nolint:gosec // the signed enum index
		if size == 2 {
			index = int(int16(binary.LittleEndian.Uint16(b))) //nolint:gosec // the signed enum index
		}
		if name, ok := members[index]; ok {
			return append(dst, name...)
		}
		return strconv.AppendInt(dst, int64(index), 10)
	})
}

// readDecimals reads Decimal32/64/128/256(S) or Decimal(P, S), each written as the scaled integer
// with S fractional digits
func (c *nativeColumn) readDecimals(cur *nativeCursor, typ string, n int) error {
	size, scale, err := decimalLayout(typ)
	if err != nil {
		return err
	}
	var digits []byte
	return c.readEach(cur, n, size, func(dst, b []byte) []byte {
		digits = appendBigInt(digits[:0], b, true)
		d := digits
		if scale == 0 {
			return append(dst, d...)
		}
		if d[0] == '-' {
			dst, d = append(dst, '-'), d[1:]
		}
		// too few digits are written as a fraction of zero, the zeros it needs first
		if len(d) <= scale {
			dst = append(dst, '0', '.')
			for range scale - len(d) {
				dst = append(dst, '0')
			}
			return append(dst, d...)
		}
		dst = append(dst, d[:len(d)-scale]...)
		return append(append(dst, '.'), d[len(d)-scale:]...)
	})
}

// decimalLayout returns the bytes a Decimal type's values take, and its scale
func decimalLayout(typ string) (size, scale int, err error) {
	args := strings.Split(strings.TrimSuffix(typ[strings.Index(typ, "(")+1:], ")"), ",")
	switch {
	case strings.HasPrefix(typ, "Decimal32("):
		size = 4
	case strings.HasPrefix(typ, "Decimal64("):
		size = 8
	case strings.HasPrefix(typ, "Decimal128("):
		size = 16
	case strings.HasPrefix(typ, "Decimal256("):
		size = 32
	case strings.HasPrefix(typ, "Decimal(") && len(args) == 2:
		precision, perr := strconv.Atoi(strings.TrimSpace(args[0]))
		if perr != nil {
			return 0, 0, fmt.Errorf("invalid Decimal type %q", typ)
		}
		switch {
		case precision <= 9:
			size = 4
		case precision <= 18:
			size = 8
		case precision <= 38:
			size = 16
		default:
			size = 32
		}
		args = args[1:]
	}
	if size == 0 || len(args) != 1 {
		return 0, 0, fmt.Errorf("invalid Decimal type %q", typ)
	}
	scale, err = strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || scale < 0 {
		return 0, 0, fmt.Errorf("invalid Decimal type %q", typ)
	}
	return size, scale, nil
}

// readLowCardinality reads the shared-dictionary layout that follows the state prefix: index flags,
// the dictionary as a plain column, then indexes
func (c *nativeColumn) readLowCardinality(cur *nativeCursor, inner string, n int) error {
	if n == 0 {
		return nil
	}
	hdr, err := cur.fixed(2 * offsetSize)
	if err != nil {
		return err
	}
	flags, size := binary.LittleEndian.Uint64(hdr), binary.LittleEndian.Uint64(hdr[offsetSize:])
	if flags&lowCardinalityGlobalDict != 0 || flags&lowCardinalityAdditional == 0 {
		return fmt.Errorf("%w: LowCardinality global dictionary", errUnsupportedSerialization)
	}
	width := uint64(1) << (flags & lowCardinalityWidthMask)
	if width > lowCardinalityMaxKeyWidth || width == 0 {
		return fmt.Errorf("%w: LowCardinality index width %d", errUnsupportedSerialization, width)
	}
	dictType, nullable := inner, false
	if t, ok := cutWrapper(inner, prefixNullable); ok {
		dictType, nullable = t, true
	}
	if size > cur.left() {
		return errShort
	}
	if c.dict == nil {
		c.dict = &nativeColumn{}
	}
	if err := c.dict.readValues(cur, dictType, int(size)); err != nil { //nolint:gosec // bounded above
		return err
	}
	count, err := cur.fixed(offsetSize)
	if err != nil {
		return err
	}
	if v := binary.LittleEndian.Uint64(count); v != uint64(n) { //nolint:gosec // a row count
		return fmt.Errorf("LowCardinality row count %d does not match block %d", v, n)
	}
	raw, err := cur.words(n, int(width)) //nolint:gosec // at most 8
	if err != nil {
		return err
	}
	c.kind = colDict
	c.words = growTo(c.words, n)
	for i := range n {
		var idx uint64
		switch width {
		case 1:
			idx = uint64(raw[i])
		case 2:
			idx = uint64(binary.LittleEndian.Uint16(raw[2*i:]))
		case 4:
			idx = uint64(binary.LittleEndian.Uint32(raw[4*i:]))
		default:
			idx = binary.LittleEndian.Uint64(raw[8*i:])
		}
		if idx >= size {
			return fmt.Errorf("LowCardinality index %d outside dictionary of %d", idx, size)
		}
		c.words[i] = idx
	}
	// a nullable dictionary's first entry is NULL
	if nullable {
		c.nulls = growTo(c.nulls, n)
		for i, idx := range c.words {
			c.nulls[i] = boolByte(idx == 0)
		}
	}
	return nil
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// elem returns the compound type's i'th element column
func (c *nativeColumn) elem(i int) *nativeColumn {
	for len(c.elems) <= i {
		c.elems = append(c.elems, &nativeColumn{})
	}
	return c.elems[i]
}

// readOffsets reads each row's end in a compound column's elements, returning the elements' count
func readOffsets(cur *nativeCursor, n int) ([]byte, int, error) {
	raw, err := cur.words(n, offsetSize)
	if err != nil {
		return nil, 0, err
	}
	var total uint64
	for i := range n {
		end := binary.LittleEndian.Uint64(raw[offsetSize*i:])
		if end < total {
			return nil, 0, errors.New("invalid compound column offsets")
		}
		total = end
	}
	if total > cur.left() {
		return nil, 0, errShort
	}
	return raw, int(total), nil //nolint:gosec // bounded above
}

// The compound types below are held as ClickHouse's text-literal form of each row ("[1,'a']",
// "('a',1)", "{'k':1}"), which is also what the TSV format gives them.

func (c *nativeColumn) readArray(cur *nativeCursor, inner string, n int) error {
	offsets, total, err := readOffsets(cur, n)
	if err != nil {
		return err
	}
	elems := c.elem(0)
	if err := elems.readValues(cur, inner, total); err != nil {
		return err
	}
	quote := quotedLiteral(inner)
	c.ends = growTo(c.ends, n)[:0]
	var start int
	for i := range n {
		end := int(binary.LittleEndian.Uint64(offsets[offsetSize*i:])) //nolint:gosec // at most total
		c.data = append(c.data, '[')
		for j := start; j < end; j++ {
			if j > start {
				c.data = append(c.data, ',')
			}
			c.data = elems.appendLiteral(c.data, j, quote)
		}
		c.data = append(c.data, ']')
		c.ends = append(c.ends, len(c.data))
		start = end
	}
	return nil
}

func (c *nativeColumn) readMap(cur *nativeCursor, inner string, n int) error {
	types := splitTypeList(inner)
	if len(types) != 2 {
		return fmt.Errorf("%w: Map(%s)", errUnsupportedColumnType, inner)
	}
	offsets, total, err := readOffsets(cur, n)
	if err != nil {
		return err
	}
	keys, vals := c.elem(0), c.elem(1)
	if err := keys.readValues(cur, types[0], total); err != nil {
		return err
	}
	if err := vals.readValues(cur, types[1], total); err != nil {
		return err
	}
	keyQuote, valQuote := quotedLiteral(types[0]), quotedLiteral(types[1])
	c.ends = growTo(c.ends, n)[:0]
	var start int
	for i := range n {
		end := int(binary.LittleEndian.Uint64(offsets[offsetSize*i:])) //nolint:gosec // at most total
		c.data = append(c.data, '{')
		for j := start; j < end; j++ {
			if j > start {
				c.data = append(c.data, ',')
			}
			c.data = keys.appendLiteral(c.data, j, keyQuote)
			c.data = vals.appendLiteral(append(c.data, ':'), j, valQuote)
		}
		c.data = append(c.data, '}')
		c.ends = append(c.ends, len(c.data))
		start = end
	}
	return nil
}

func (c *nativeColumn) readTuple(cur *nativeCursor, inner string, n int) error {
	types := splitTypeList(inner)
	quotes := make([]literalKind, len(types))
	for i, t := range types {
		t = elementType(t)
		if err := c.elem(i).readValues(cur, t, n); err != nil {
			return err
		}
		quotes[i] = quotedLiteral(t)
	}
	c.ends = growTo(c.ends, n)[:0]
	for r := range n {
		c.data = append(c.data, '(')
		for i := range types {
			if i > 0 {
				c.data = append(c.data, ',')
			}
			c.data = c.elems[i].appendLiteral(c.data, r, quotes[i])
		}
		c.data = append(c.data, ')')
		c.ends = append(c.ends, len(c.data))
	}
	return nil
}

// literalKind is how an element of a compound value is written: as its text, quoted, or as a bool
type literalKind uint8

const (
	literalBare literalKind = iota
	literalQuoted
	literalBool
)

func quotedLiteral(typ string) literalKind {
	switch stripSize(unwrapColumnType(typ)) {
	case TypeString, "FixedString", typeUUID, "Enum8", "Enum16", typeIPv4, typeIPv6, TypeDate, typeDate32,
		TypeDateTime, prefixDateTime64:
		return literalQuoted
	case TypeBool:
		return literalBool
	}
	return literalBare
}

// appendLiteral appends row r as ClickHouse prints it within a compound value: text-like types are
// single-quoted and escaped, and NULL stays bare
func (c *nativeColumn) appendLiteral(dst []byte, r int, kind literalKind) []byte {
	val := c.text(&c.scratch, r)
	if string(val) == nullToken {
		return append(dst, nullLiteral...)
	}
	switch kind {
	case literalQuoted:
		dst = append(dst, '\'')
		for _, ch := range val {
			switch ch {
			case '\'', '\\':
				dst = append(dst, '\\', ch)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\t':
				dst = append(dst, '\\', 't')
			case 0:
				dst = append(dst, '\\', '0')
			default:
				dst = append(dst, ch)
			}
		}
		return append(dst, '\'')
	case literalBool:
		if string(val) == "1" {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	}
	return append(dst, val...)
}

// nullAt reports whether row r is NULL, in the column or, for a dictionary's keys, its dictionary
func (c *nativeColumn) nullAt(r int) bool {
	for c.kind == colDict && !c.null(r) {
		c, r = c.dict, int(c.words[r]) //nolint:gosec // an index checked against the dictionary
	}
	return c.null(r)
}

func (c *nativeColumn) null(r int) bool {
	return len(c.nulls) > 0 && c.nulls[r] != 0
}

// text returns row r's value as its TSV text: the column's own bytes, or scratch holding it
func (c *nativeColumn) text(scratch *[]byte, r int) []byte {
	if c.null(r) {
		return nullText
	}
	switch c.kind {
	case colText:
		var start int
		if r > 0 {
			start = c.ends[r-1]
		}
		return c.data[start:c.ends[r]]
	case colDict:
		return c.dict.text(scratch, int(c.words[r])) //nolint:gosec // an index checked against the dictionary
	}
	b, w := (*scratch)[:0], c.words[r]
	switch c.kind {
	case colInt:
		b = strconv.AppendInt(b, int64(w), 10) //nolint:gosec // the wire's int64
	case colUint, colBool:
		b = strconv.AppendUint(b, w, 10)
	case colFloat64:
		b = strconv.AppendFloat(b, math.Float64frombits(w), 'g', -1, 64)
	case colFloat32:
		b = strconv.AppendFloat(b, float64(math.Float32frombits(uint32(w))), 'g', -1, 32) //nolint:gosec // a uint32
	case colDateTime:
		b = epoch.Epoch(int64(w)*int64(epoch.BillionNS)).AppendFormat(b, timeseries.DateTimeSQL, false) //nolint:gosec // a uint32
	case colDate:
		b = epoch.Epoch(int64(w)*secondsPerDay*int64(epoch.BillionNS)).AppendFormat(b, timeseries.DateSQL, false) //nolint:gosec // a uint16
	case colDate32:
		b = time.Unix(0, 0).UTC().AddDate(0, 0, int(int64(w))).AppendFormat(b, dateLayout) //nolint:gosec // an int32
	case colDateTime64:
		v, scale := int64(w), pow10[c.precision] //nolint:gosec // the wire's int64
		b = time.Unix(v/scale, v%scale*pow10[maxPrecision-c.precision]).UTC().AppendFormat(b, c.layout)
	}
	*scratch = b
	return b
}

// add adds row r's value as its field's type reads the value's text, from the value itself when
// the column's type and the field's give the same value
func (c *nativeColumn) add(rb *dataset.RowBuilder, dt timeseries.FieldDataType, r int, scratch *[]byte) {
	for c.kind == colDict && !c.null(r) {
		c, r = c.dict, int(c.words[r]) //nolint:gosec // an index checked against the dictionary
	}
	if c.null(r) {
		rb.AddNull()
		return
	}
	switch w := c.words; {
	case c.kind == colInt && dt == timeseries.Int64:
		rb.AddInt64(int64(w[r])) //nolint:gosec // the wire's int64
		return
	case c.kind == colUint && dt == timeseries.Uint64:
		rb.AddUint64(w[r])
		return
	case c.kind == colFloat64 && dt == timeseries.Float64:
		v := math.Float64frombits(w[r])
		// its text reads as the one NaN
		if math.IsNaN(v) {
			v = math.NaN()
		}
		rb.AddFloat64(v)
		return
	case c.kind == colBool && dt == timeseries.Bool && w[r] <= 1:
		rb.AddBool(w[r] == 1)
		return
	}
	addText(rb, c.text(scratch, r), dt)
}

// parseEnumMembers parses "Enum8('a' = 1, 'b\'c' = 2)" into {1: a, 2: b'c}.
func parseEnumMembers(typ string) map[int]string {
	out := map[int]string{}
	body := typ[strings.Index(typ, "(")+1:]
	body = strings.TrimSuffix(body, ")")
	for len(body) > 0 {
		start := strings.IndexByte(body, '\'')
		if start < 0 {
			break
		}
		var name strings.Builder
		i := start + 1
		for i < len(body) {
			c := body[i]
			if c == '\\' && i+1 < len(body) {
				name.WriteByte(body[i+1])
				i += 2
				continue
			}
			if c == '\'' {
				break
			}
			name.WriteByte(c)
			i++
		}
		rest := body[min(i+1, len(body)):]
		end := strings.IndexByte(rest, ',')
		if end < 0 {
			end = len(rest)
		}
		if eq := strings.IndexByte(rest[:end], '='); eq >= 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(rest[eq+1 : end])); err == nil {
				out[n] = name.String()
			}
		}
		body = rest[min(end+1, len(rest)):]
	}
	return out
}

// splitTypeList splits "K, V" or "T1, T2" at top-level commas.
func splitTypeList(input string) []string {
	var out []string
	depth, start, quoted := 0, 0, false
	for i, c := range input {
		switch {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(input[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(input[start:]))
}
