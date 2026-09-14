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
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	dcsv "github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/csv"
)

// nativeParser uses the same CSV→DataSet pipeline but with rows built from
// Native binary blocks instead of TSV text.
var nativeParser = dcsv.NewParserMust(buildFieldDefinitions, typeToFieldDataType,
	parseTimeField, dataStartRow)

// UnmarshalTimeseriesNative decodes a ClickHouse Native binary response into
// a Timeseries (DataSet). The Native format is: block info, then per-column
// (name, type, data[numRows]). We decode the columnar data into rows and
// feed the same CSV→DataSet pipeline used by TSV.
func UnmarshalTimeseriesNative(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesNativeReader(bufio.NewReader(
		io.NopCloser(&nopReader{data, 0})), trq)
}

type nopReader struct {
	data []byte
	pos  int
}

func (r *nopReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// UnmarshalTimeseriesNativeReader decodes a ClickHouse Native binary response
// from an io.Reader into a Timeseries (DataSet). ClickHouse sends multiple
// blocks for large result sets; we read all of them.
func UnmarshalTimeseriesNativeReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	br := bufio.NewReaderSize(reader, 128*1024)

	var colNames, colTypes []string
	var allRows [][]string
	var hasBlockInfo bool

	for {
		// Peek to detect block info header. When client_protocol_version is
		// present, ClickHouse includes TCP-style block info (starts with
		// field_num 1 = is_overflows). Without it, the response starts
		// directly with numCols (uvarint > 1 for any useful query).
		peek, err := br.Peek(1)
		if err != nil {
			break
		}
		if peek[0] == 1 {
			hasBlockInfo = true
			if err := skipBlockInfo(br); err != nil {
				break
			}
		}

		numCols, err := readUvarint(br)
		if err != nil {
			break
		}
		numRows, err := readUvarint(br)
		if err != nil {
			break
		}
		if numCols == 0 || numRows == 0 {
			break
		}

		blockColNames := make([]string, numCols)
		blockColTypes := make([]string, numCols)
		columns := make([][]string, numCols)

		for c := range numCols {
			name, err := readString(br)
			if err != nil {
				return nil, fmt.Errorf("native: column %d name: %w", c, err)
			}
			blockColNames[c] = name

			typ, err := readString(br)
			if err != nil {
				return nil, fmt.Errorf("native: column %d type: %w", c, err)
			}
			blockColTypes[c] = typ

			// When block info is present (TCP-style format via
			// client_protocol_version), a customSerialization bool follows
			// each column type header; a set flag means a serialization
			// state prefix this reader does not understand.
			if hasBlockInfo {
				flag, err := br.ReadByte()
				if err != nil {
					return nil, fmt.Errorf("native: column %d custom serialization flag: %w", c, err)
				}
				if flag != 0 && !strings.HasPrefix(strings.TrimSpace(typ), "LowCardinality(") {
					return nil, fmt.Errorf("native: column %q: %w", name, errUnsupportedSerialization)
				}
			}

			vals, err := readColumn(br, typ, numRows)
			if err != nil {
				return nil, fmt.Errorf("native: column %q: %w", name, err)
			}
			columns[c] = vals
		}

		if colNames == nil {
			colNames = blockColNames
			colTypes = blockColTypes
		}

		for r := range numRows {
			row := make([]string, numCols)
			for c := range numCols {
				row[c] = columns[c][r]
			}
			allRows = append(allRows, row)
		}
	}

	if colNames == nil || len(allRows) == 0 {
		return nil, timeseries.ErrInvalidBody
	}

	rows := make([][]string, dataStartRow+len(allRows))
	rows[0] = colNames
	rows[1] = colTypes
	copy(rows[dataStartRow:], allRows)

	return nativeParser.ToDataSet(rows, trq)
}

func skipBlockInfo(r *bufio.Reader) error {
	for {
		fieldNum, err := readUvarint(r)
		if err != nil {
			return err
		}
		if fieldNum == 0 {
			break
		}
		switch fieldNum {
		case 1:
			_, err = r.ReadByte()
		case 2:
			_, err = readFixed(r, 4)
		default:
			return fmt.Errorf("unknown block info field: %d", fieldNum)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------- Native binary reading primitives ----------

func readUvarint(r io.ByteReader) (uint64, error) {
	return binary.ReadUvarint(r)
}

func readString(r *bufio.Reader) (string, error) {
	n, err := readUvarint(r)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return string(b), err
}

// readFixed reads exactly n bytes of a framed wire value, leaving whatever
// follows for the next read. It is a local name for the shared bounded read
// so the many fixed-width call sites below stay readable.
func readFixed(r io.Reader, n int) ([]byte, error) {
	return tbytes.ReadBoundedBody(r, n, true)
}

// nullToken is the NULL marker in decoded rows; it matches ClickHouse's own
// TSV null literal so both wire formats produce identical datasets.
//
//nolint:gosec // intentional wire protocol conversions
const nullToken = "\\N"

var (
	errUnsupportedSerialization = errors.New("custom column serialization is not supported")
	errUnsupportedColumnType    = errors.New("unsupported native column type")
)

// readColumn reads a column's serialization state prefix (LowCardinality
// members declare their dictionary version there, even when nested) and
// then its values.
func readColumn(r *bufio.Reader, typ string, numRows uint64) ([]string, error) {
	if numRows > 0 {
		if err := readStatePrefix(r, strings.TrimSpace(typ)); err != nil {
			return nil, err
		}
	}
	return readColumnValues(r, typ, numRows)
}

func readStatePrefix(r *bufio.Reader, typ string) error {
	inner := func(prefix string) string { return typ[len(prefix) : len(typ)-1] }
	switch {
	case strings.HasPrefix(typ, "LowCardinality("):
		v, err := readUInt64s(r, 1)
		if err != nil {
			return err
		}
		if v[0] != 1 {
			return fmt.Errorf("%w: LowCardinality key version %d", errUnsupportedSerialization, v[0])
		}
		return nil
	case strings.HasPrefix(typ, "Nullable("):
		return readStatePrefix(r, inner("Nullable("))
	case strings.HasPrefix(typ, "Array("):
		return readStatePrefix(r, inner("Array("))
	case strings.HasPrefix(typ, "Map("), strings.HasPrefix(typ, "Tuple("):
		for _, t := range splitTypeList(typ[strings.IndexByte(typ, '(')+1 : len(typ)-1]) {
			if sp := strings.IndexByte(t, ' '); sp > 0 && !strings.ContainsAny(t[:sp], "()'") {
				t = strings.TrimSpace(t[sp+1:])
			}
			if err := readStatePrefix(r, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// readColumnValues decodes one column's values. Nullable columns carry a
// null map before the values; compound encodings (LowCardinality, Array,
// Map, Tuple, Nested, Variant, Dynamic, JSON) are rejected rather than
// misread, because their layout is not one value per row.
func readColumnValues(r *bufio.Reader, typ string, numRows uint64) ([]string, error) {
	typ = strings.TrimSpace(typ)
	if strings.HasPrefix(typ, "Nullable(") && strings.HasSuffix(typ, ")") {
		nulls, err := readFixed(r, int(numRows)) //nolint:gosec // bounded by the block row count
		if err != nil {
			return nil, err
		}
		vals, err := readColumnValues(r, typ[len("Nullable("):len(typ)-1], numRows)
		if err != nil {
			return nil, err
		}
		for i, isNull := range nulls {
			if isNull != 0 {
				vals[i] = nullToken
			}
		}
		return vals, nil
	}
	switch {
	case strings.HasPrefix(typ, "LowCardinality(") && strings.HasSuffix(typ, ")"):
		return readLowCardinality(r, typ[len("LowCardinality("):len(typ)-1], numRows)
	case strings.HasPrefix(typ, "Array(") && strings.HasSuffix(typ, ")"):
		return readArray(r, typ[len("Array("):len(typ)-1], numRows)
	case strings.HasPrefix(typ, "Map(") && strings.HasSuffix(typ, ")"):
		return readMap(r, typ[len("Map("):len(typ)-1], numRows)
	case strings.HasPrefix(typ, "Tuple(") && strings.HasSuffix(typ, ")"):
		return readTuple(r, typ[len("Tuple("):len(typ)-1], numRows)
	}
	for _, compound := range []string{
		"Nested(", "Variant(", "Dynamic", "JSON", "Object(",
		"AggregateFunction(", "SimpleAggregateFunction(",
	} {
		if strings.HasPrefix(typ, compound) {
			return nil, fmt.Errorf("%w: %s", errUnsupportedColumnType, typ)
		}
	}
	vals := make([]string, numRows)
	for i := range numRows {
		v, err := readValueAsString(r, typ)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i, err)
		}
		vals[i] = v
	}
	return vals, nil
}

func readValueAsString(r *bufio.Reader, typ string) (string, error) {
	switch typ {
	case TypeUInt8, TypeBool:
		b, err := r.ReadByte()
		return strconv.FormatUint(uint64(b), 10), err
	case TypeUInt16:
		b, err := readFixed(r, 2)
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(uint64(binary.LittleEndian.Uint16(b)), 10), nil
	case TypeUInt32:
		b, err := readFixed(r, 4)
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b)), 10), nil
	case TypeUInt64:
		b, err := readFixed(r, 8)
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(binary.LittleEndian.Uint64(b), 10), nil
	case TypeInt8:
		b, err := r.ReadByte()
		return strconv.Itoa(int(int8(b))), err
	case TypeInt16:
		b, err := readFixed(r, 2)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(int16(binary.LittleEndian.Uint16(b)))), nil
	case TypeInt32:
		b, err := readFixed(r, 4)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(int32(binary.LittleEndian.Uint32(b)))), nil
	case TypeInt64:
		b, err := readFixed(r, 8)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(int64(binary.LittleEndian.Uint64(b)), 10), nil
	case TypeFloat32:
		b, err := readFixed(r, 4)
		if err != nil {
			return "", err
		}
		return fmt.Sprint(math.Float32frombits(binary.LittleEndian.Uint32(b))), nil
	case TypeFloat64:
		b, err := readFixed(r, 8)
		if err != nil {
			return "", err
		}
		return fmt.Sprint(math.Float64frombits(binary.LittleEndian.Uint64(b))), nil
	case TypeDateTime:
		b, err := readFixed(r, 4)
		if err != nil {
			return "", err
		}
		ts := binary.LittleEndian.Uint32(b)
		return time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04:05"), nil
	case TypeDate:
		b, err := readFixed(r, 2)
		if err != nil {
			return "", err
		}
		days := binary.LittleEndian.Uint16(b)
		t := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days))
		return t.Format("2006-01-02"), nil
	case TypeString:
		return readString(r)
	default:
		if strings.HasPrefix(typ, "DateTime64") {
			b, err := readFixed(r, 8)
			if err != nil {
				return "", err
			}
			precisionText := strings.TrimSpace(strings.Split(
				strings.TrimSuffix(strings.TrimPrefix(typ, "DateTime64("), ")"), ",",
			)[0])
			precision, err := strconv.Atoi(precisionText)
			if err != nil || precision < 0 || precision > 9 {
				return "", fmt.Errorf("invalid DateTime64 precision %q", precisionText)
			}
			v := int64(binary.LittleEndian.Uint64(b))
			scale := int64(math.Pow10(precision))
			t := time.Unix(v/scale, (v%scale)*int64(math.Pow10(9-precision))).UTC()
			layout := "2006-01-02 15:04:05"
			if precision > 0 {
				layout += "." + strings.Repeat("0", precision)
			}
			return t.Format(layout), nil
		}
		if strings.HasPrefix(typ, "DateTime(") {
			b, err := readFixed(r, 4)
			if err != nil {
				return "", err
			}
			ts := binary.LittleEndian.Uint32(b)
			return time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04:05"), nil
		}
		if strings.HasPrefix(typ, "Enum8(") || strings.HasPrefix(typ, "Enum16(") {
			return readEnumValue(r, typ)
		}
		if strings.HasPrefix(typ, "Decimal") {
			return readDecimalValue(r, typ)
		}
		switch typ {
		case "Date32":
			b, err := readFixed(r, 4)
			if err != nil {
				return "", err
			}
			days := int32(binary.LittleEndian.Uint32(b)) //nolint:gosec // wire bytes reinterpreted as signed days
			return time.Unix(0, 0).UTC().AddDate(0, 0, int(days)).Format("2006-01-02"), nil
		case "Int128", "UInt128":
			return readBigInt(r, 16, typ[0] == 'I')
		case "Int256", "UInt256":
			return readBigInt(r, 32, typ[0] == 'I')
		case "UUID":
			b, err := readFixed(r, 16)
			if err != nil {
				return "", err
			}
			// ClickHouse stores a UUID as two little-endian UInt64 halves.
			var u [16]byte
			for i := range 8 {
				u[i] = b[7-i]
				u[8+i] = b[15-i]
			}
			return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
		case "IPv4":
			b, err := readFixed(r, 4)
			if err != nil {
				return "", err
			}
			return net.IPv4(b[3], b[2], b[1], b[0]).String(), nil
		case "IPv6":
			b, err := readFixed(r, 16)
			if err != nil {
				return "", err
			}
			return net.IP(b).String(), nil
		}
		if after, ok := strings.CutPrefix(typ, "FixedString("); ok {
			n, err := strconv.Atoi(strings.TrimSuffix(after, ")"))
			if err != nil || n < 0 {
				return "", fmt.Errorf("invalid FixedString length in %q", typ)
			}
			b, err := readFixed(r, n)
			if err != nil {
				return "", err
			}
			return string(bytes.TrimRight(b, "\x00")), nil
		}
		return readString(r)
	}
}

// enumMembers caches the parsed member table for each Enum type string so
// the per-row decoder does not re-parse the declaration.
var enumMembers sync.Map

// readEnumValue reads an Enum8/Enum16 index and returns its member name, or
// the bare index when the declaration does not list it.
func readEnumValue(r *bufio.Reader, typ string) (string, error) {
	var index int
	if strings.HasPrefix(typ, "Enum8(") {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		index = int(int8(b)) //nolint:gosec // wire byte reinterpreted as the signed enum index
	} else {
		b, err := readFixed(r, 2)
		if err != nil {
			return "", err
		}
		index = int(int16(binary.LittleEndian.Uint16(b))) //nolint:gosec // wire bytes reinterpreted as the signed enum index
	}
	members, ok := enumMembers.Load(typ)
	if !ok {
		members, _ = enumMembers.LoadOrStore(typ, parseEnumMembers(typ))
	}
	if name, ok := members.(map[int]string)[index]; ok {
		return name, nil
	}
	return strconv.Itoa(index), nil
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

// readBigInt reads a little-endian two's-complement integer of n bytes.
func readBigInt(r *bufio.Reader, n int, signed bool) (string, error) {
	b, err := readFixed(r, n)
	if err != nil {
		return "", err
	}
	be := make([]byte, n)
	for i := range n {
		be[i] = b[n-1-i]
	}
	v := new(big.Int).SetBytes(be)
	if signed && be[0]&0x80 != 0 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), uint(8*n))) //nolint:gosec // n is 16 or 32
	}
	return v.String(), nil
}

// readDecimalValue reads Decimal32/64/128/256(S) or Decimal(P, S) and formats
// the scaled integer with S fractional digits.
func readDecimalValue(r *bufio.Reader, typ string) (string, error) {
	args := strings.Split(strings.TrimSuffix(typ[strings.Index(typ, "(")+1:], ")"), ",")
	width := 0
	switch {
	case strings.HasPrefix(typ, "Decimal32("):
		width = 4
	case strings.HasPrefix(typ, "Decimal64("):
		width = 8
	case strings.HasPrefix(typ, "Decimal128("):
		width = 16
	case strings.HasPrefix(typ, "Decimal256("):
		width = 32
	case strings.HasPrefix(typ, "Decimal(") && len(args) == 2:
		precision, perr := strconv.Atoi(strings.TrimSpace(args[0]))
		if perr != nil {
			return "", fmt.Errorf("invalid Decimal type %q", typ)
		}
		switch {
		case precision <= 9:
			width = 4
		case precision <= 18:
			width = 8
		case precision <= 38:
			width = 16
		default:
			width = 32
		}
		args = args[1:]
	}
	if width == 0 || len(args) != 1 {
		return "", fmt.Errorf("invalid Decimal type %q", typ)
	}
	scale, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || scale < 0 {
		return "", fmt.Errorf("invalid Decimal type %q", typ)
	}
	text, err := readBigInt(r, width, true)
	if err != nil {
		return "", err
	}
	if scale == 0 {
		return text, nil
	}
	neg := strings.HasPrefix(text, "-")
	digits := strings.TrimPrefix(text, "-")
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	out := digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	if neg {
		out = "-" + out
	}
	return out, nil
}

// The compound decoders below return ClickHouse's text-literal form for each
// row ("[1,'a']", "('a',1)", "{'k':1}"), which is also what the TSV path
// stores, so the Native encoder only has to understand one representation.

func readUInt64s(r *bufio.Reader, n uint64) ([]uint64, error) {
	b, err := readFixed(r, int(8*n)) //nolint:gosec // bounded by the block row count
	if err != nil {
		return nil, err
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(b[8*i:])
	}
	return out, nil
}

// literal renders one element of a compound value the way ClickHouse prints
// it: text-like types are single-quoted and escaped, NULL stays bare.
func literal(typ, val string) string {
	if val == nullToken {
		return "NULL"
	}
	switch stripSize(unwrapColumnType(typ)) {
	case "String", "FixedString", "UUID", "Enum8", "Enum16", "IPv4", "IPv6",
		"Date", "Date32", "DateTime", "DateTime64":
		var b strings.Builder
		b.Grow(len(val) + 2)
		b.WriteByte('\'')
		for i := range len(val) {
			switch c := val[i]; c {
			case '\'', '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case '\n':
				b.WriteString("\\n")
			case '\t':
				b.WriteString("\\t")
			case 0:
				b.WriteString("\\0")
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('\'')
		return b.String()
	case "Bool":
		if val == "1" {
			return "true"
		}
		return "false"
	}
	return val
}

func joinLiterals(typ string, vals []string, open, closer byte) string {
	var b strings.Builder
	b.WriteByte(open)
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(literal(typ, v))
	}
	b.WriteByte(closer)
	return b.String()
}

func readOffsets(r *bufio.Reader, numRows uint64) ([]uint64, uint64, error) {
	offsets, err := readUInt64s(r, numRows)
	if err != nil {
		return nil, 0, err
	}
	var total uint64
	for _, end := range offsets {
		if end < total {
			return nil, 0, errors.New("invalid compound column offsets")
		}
		total = end
	}
	return offsets, total, nil
}

func readArray(r *bufio.Reader, inner string, numRows uint64) ([]string, error) {
	offsets, total, err := readOffsets(r, numRows)
	if err != nil {
		return nil, err
	}
	elems, err := readColumnValues(r, inner, total)
	if err != nil {
		return nil, err
	}
	out := make([]string, numRows)
	var start uint64
	for i, end := range offsets {
		out[i] = joinLiterals(inner, elems[start:end], '[', ']')
		start = end
	}
	return out, nil
}

func readMap(r *bufio.Reader, inner string, numRows uint64) ([]string, error) {
	types := splitTypeList(inner)
	if len(types) != 2 {
		return nil, fmt.Errorf("%w: Map(%s)", errUnsupportedColumnType, inner)
	}
	offsets, total, err := readOffsets(r, numRows)
	if err != nil {
		return nil, err
	}
	keys, err := readColumnValues(r, types[0], total)
	if err != nil {
		return nil, err
	}
	vals, err := readColumnValues(r, types[1], total)
	if err != nil {
		return nil, err
	}
	out := make([]string, numRows)
	var start uint64
	for i, end := range offsets {
		var b strings.Builder
		b.WriteByte('{')
		for j := start; j < end; j++ {
			if j > start {
				b.WriteByte(',')
			}
			b.WriteString(literal(types[0], keys[j]))
			b.WriteByte(':')
			b.WriteString(literal(types[1], vals[j]))
		}
		b.WriteByte('}')
		out[i] = b.String()
		start = end
	}
	return out, nil
}

func readTuple(r *bufio.Reader, inner string, numRows uint64) ([]string, error) {
	types := splitTypeList(inner)
	columns := make([][]string, len(types))
	for i, t := range types {
		// named tuple elements are declared as "name Type"
		if sp := strings.IndexByte(t, ' '); sp > 0 && !strings.ContainsAny(t[:sp], "()'") {
			t = strings.TrimSpace(t[sp+1:])
			types[i] = t
		}
		vals, err := readColumnValues(r, t, numRows)
		if err != nil {
			return nil, err
		}
		columns[i] = vals
	}
	out := make([]string, numRows)
	for row := range numRows {
		var b strings.Builder
		b.WriteByte('(')
		for i := range types {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(literal(types[i], columns[i][row]))
		}
		b.WriteByte(')')
		out[row] = b.String()
	}
	return out, nil
}

// readLowCardinality decodes the shared-dictionary layout that follows the
// state prefix: index flags, the dictionary as a plain column, then indexes.
func readLowCardinality(r *bufio.Reader, inner string, numRows uint64) ([]string, error) {
	if numRows == 0 {
		return []string{}, nil
	}
	hdr, err := readUInt64s(r, 2) // index flags, dictionary size
	if err != nil {
		return nil, err
	}
	if hdr[0]&(1<<8) != 0 || hdr[0]&(1<<9) == 0 {
		return nil, fmt.Errorf("%w: LowCardinality global dictionary", errUnsupportedSerialization)
	}
	keyWidth := 1 << (hdr[0] & 0xff)
	if keyWidth > 8 {
		return nil, fmt.Errorf("%w: LowCardinality index width %d", errUnsupportedSerialization, keyWidth)
	}
	dictType := inner
	nullable := false
	if strings.HasPrefix(inner, "Nullable(") && strings.HasSuffix(inner, ")") {
		dictType = inner[len("Nullable(") : len(inner)-1]
		nullable = true
	}
	dict, err := readColumnValues(r, dictType, hdr[1])
	if err != nil {
		return nil, err
	}
	count, err := readUInt64s(r, 1)
	if err != nil {
		return nil, err
	}
	if count[0] != numRows {
		return nil, fmt.Errorf("LowCardinality row count %d does not match block %d", count[0], numRows)
	}
	raw, err := readFixed(r, keyWidth*int(numRows)) //nolint:gosec // bounded by the block row count
	if err != nil {
		return nil, err
	}
	out := make([]string, numRows)
	for i := range out {
		var idx uint64
		switch keyWidth {
		case 1:
			idx = uint64(raw[i])
		case 2:
			idx = uint64(binary.LittleEndian.Uint16(raw[2*i:]))
		case 4:
			idx = uint64(binary.LittleEndian.Uint32(raw[4*i:]))
		default:
			idx = binary.LittleEndian.Uint64(raw[8*i:])
		}
		if idx >= uint64(len(dict)) {
			return nil, fmt.Errorf("LowCardinality index %d outside dictionary of %d", idx, len(dict))
		}
		if nullable && idx == 0 {
			out[i] = nullToken
			continue
		}
		out[i] = dict[idx]
	}
	return out, nil
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
