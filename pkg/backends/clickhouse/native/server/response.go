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
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/ClickHouse/ch-go/compress"
)

// writeException sends a ServerException packet.
func writeException(w *protoWriter, code int32, name, message string) error {
	if err := w.putByte(ServerException); err != nil {
		return err
	}
	if err := w.putInt32(code); err != nil {
		return err
	}
	if err := w.putStr(name); err != nil {
		return err
	}
	if err := w.putStr(message); err != nil {
		return err
	}
	if err := w.putStr(""); err != nil { // stack trace
		return err
	}
	return w.putBool(false) // has nested
}

// writeEndOfStream sends a ServerEndOfStream packet.
func writeEndOfStream(w *protoWriter) error {
	return w.putByte(ServerEndOfStream)
}

// writePong sends a ServerPong packet.
func writePong(w *protoWriter) error {
	return w.putByte(ServerPong)
}

// Column represents a named and typed column for writing data blocks.
type Column struct {
	Name string
	Type string
}

// writeDataBlock writes a ServerData packet containing a result block.
// values is a column-major 2D slice: values[col][row].
func writeDataBlock(w *protoWriter, columns []Column, values [][]any, numRows uint64) error {
	if err := w.putByte(ServerData); err != nil {
		return err
	}
	if err := w.putStr(""); err != nil {
		return err
	}
	return writeBlockContent(w, columns, values, numRows)
}

// writeEmptyBlock sends an empty ServerData block (0 columns, 0 rows).
func writeEmptyBlock(w *protoWriter) error {
	return writeDataBlock(w, nil, nil, 0)
}

// writeCompressedDataBlock writes a ServerData packet with LZ4-compressed
// block content.
func writeCompressedDataBlock(w *protoWriter, columns []Column, values [][]any, numRows uint64) error {
	if err := w.putByte(ServerData); err != nil {
		return err
	}
	if err := w.putStr(""); err != nil {
		return err
	}

	var buf bytes.Buffer
	bw := newProtoWriter(&buf)
	if err := writeBlockContent(bw, columns, values, numRows); err != nil {
		return err
	}

	cw := compress.NewWriter(compress.LevelZero, compress.LZ4)
	if err := cw.Compress(buf.Bytes()); err != nil {
		return fmt.Errorf("compress block: %w", err)
	}
	_, err := w.Write(cw.Data)
	return err
}

// writeBlockContent writes block info + columns + data (no packet header).
func writeBlockContent(w *protoWriter, columns []Column, values [][]any, numRows uint64) error {
	if err := w.putUvarint(0); err != nil { // is_overflows
		return err
	}
	if err := w.putBool(false); err != nil { // bucket_num
		return err
	}
	if err := w.putUvarint(2); err != nil { // bucket_size
		return err
	}
	if err := w.putInt32(-1); err != nil { // reserved
		return err
	}
	if err := w.putUvarint(0); err != nil { // reserved
		return err
	}

	numCols := uint64(len(columns))
	if err := w.putUvarint(numCols); err != nil {
		return err
	}
	if err := w.putUvarint(numRows); err != nil {
		return err
	}

	for i, col := range columns {
		if err := w.putStr(col.Name); err != nil {
			return err
		}
		if err := w.putStr(col.Type); err != nil {
			return err
		}
		if err := w.putBool(false); err != nil { // custom serialization flag
			return err
		}
		if numRows > 0 {
			if err := writeColumnData(w, col.Type, values[i]); err != nil {
				return fmt.Errorf("write column %q data: %w", col.Name, err)
			}
		}
	}
	return nil
}

// writeColumnData writes the raw column data for a single column.
//
//nolint:gosec // intentional wire protocol conversions
func writeColumnData(w *protoWriter, colType string, values []any) error {
	// Nullable(T): null bitmap + inner column data
	if inner, ok := strings.CutPrefix(colType, "Nullable("); ok {
		inner = strings.TrimSuffix(inner, ")")
		for _, v := range values {
			if v == nil {
				if err := w.putByte(1); err != nil {
					return err
				}
			} else {
				if err := w.putByte(0); err != nil {
					return err
				}
			}
		}
		for _, v := range values {
			if v == nil {
				v = zeroForType(inner)
			}
			if err := writeValue(w, inner, v); err != nil {
				return err
			}
		}
		return nil
	}

	// Array(T): cumulative offsets (UInt64) + flattened inner data
	if inner, ok := strings.CutPrefix(colType, "Array("); ok {
		inner = strings.TrimSuffix(inner, ")")
		var offset uint64
		var flattened []any
		for _, v := range values {
			arr := toSlice(v)
			offset += uint64(len(arr))
			var b [8]byte
			binary.LittleEndian.PutUint64(b[:], offset)
			if _, err := w.Write(b[:]); err != nil {
				return err
			}
			flattened = append(flattened, arr...)
		}
		return writeColumnData(w, inner, flattened)
	}

	// Map(K, V): offsets + keys + values
	if rest, ok := strings.CutPrefix(colType, "Map("); ok {
		rest = strings.TrimSuffix(rest, ")")
		keyType, valType := splitMapTypes(rest)
		var offset uint64
		var allKeys, allVals []any
		for _, v := range values {
			m := toMap(v)
			offset += uint64(len(m))
			var b [8]byte
			binary.LittleEndian.PutUint64(b[:], offset)
			if _, err := w.Write(b[:]); err != nil {
				return err
			}
			for mk, mv := range m {
				allKeys = append(allKeys, mk)
				allVals = append(allVals, mv)
			}
		}
		if err := writeColumnData(w, keyType, allKeys); err != nil {
			return err
		}
		return writeColumnData(w, valType, allVals)
	}

	// Tuple(T1, T2, ...): each sub-type written as a column
	if rest, ok := strings.CutPrefix(colType, "Tuple("); ok {
		rest = strings.TrimSuffix(rest, ")")
		subTypes := splitTupleTypes(rest)
		for colIdx, st := range subTypes {
			subVals := make([]any, len(values))
			for rowIdx, v := range values {
				subVals[rowIdx] = tupleElement(v, colIdx)
			}
			if err := writeColumnData(w, st, subVals); err != nil {
				return err
			}
		}
		return nil
	}

	// FixedString(N): exactly N bytes per row
	if rest, ok := strings.CutPrefix(colType, "FixedString("); ok {
		rest = strings.TrimSuffix(rest, ")")
		n := parseInt(rest)
		for _, v := range values {
			b := []byte(toString(v))
			if len(b) >= n {
				b = b[:n]
			} else {
				b = append(b, make([]byte, n-len(b))...)
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		return nil
	}

	// LowCardinality(T): dictionary + index
	if inner, ok := strings.CutPrefix(colType, "LowCardinality("); ok {
		inner = strings.TrimSuffix(inner, ")")
		return writeLowCardinality(w, inner, values)
	}

	// Default: per-value encoding
	for _, v := range values {
		if err := writeValue(w, colType, v); err != nil {
			return err
		}
	}
	return nil
}

// zeroForType returns the zero value for a ClickHouse type.
func zeroForType(colType string) any {
	switch colType {
	case "UInt8", "Int8":
		return uint8(0)
	case "UInt16", "Int16":
		return uint16(0)
	case "UInt32", "Int32", "DateTime":
		return uint32(0)
	case "UInt64", "Int64", "DateTime64":
		return uint64(0)
	case "Float32":
		return float32(0)
	case "Float64":
		return float64(0)
	default:
		return ""
	}
}

func toSlice(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return nil
}

func toMap(v any) map[string]any {
	if x, ok := v.(map[string]any); ok {
		return x
	}
	return nil
}

func splitMapTypes(s string) (string, string) {
	depth := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
			}
		}
	}
	return s, ""
}

func splitTupleTypes(s string) []string {
	var result []string
	depth := 0
	start := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				result = append(result, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if start < len(s) {
		result = append(result, strings.TrimSpace(s[start:]))
	}
	return result
}

func tupleElement(v any, idx int) any {
	if x, ok := v.([]any); ok {
		if idx < len(x) {
			return x[idx]
		}
	}
	return nil
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// writeLowCardinality writes a LowCardinality(T) column using dictionary encoding.
//
//nolint:gosec // intentional wire protocol conversions
func writeLowCardinality(w *protoWriter, innerType string, values []any) error {
	dict := make(map[string]int)
	var dictValues []any
	indices := make([]int, len(values))
	for i, v := range values {
		key := fmt.Sprint(v)
		idx, ok := dict[key]
		if !ok {
			idx = len(dictValues)
			dict[key] = idx
			dictValues = append(dictValues, v)
		}
		indices[i] = idx
	}

	if err := w.putInt64(1); err != nil { // serialization version
		return err
	}

	var indexType uint64
	switch {
	case len(dictValues) <= 256:
		indexType = 0
	case len(dictValues) <= 65536:
		indexType = 1
	default:
		indexType = 2
	}

	flags := indexType | (1 << 9) // need_update_dictionary=1
	if err := w.putInt64(int64(flags)); err != nil {
		return err
	}

	if err := w.putInt64(int64(len(dictValues))); err != nil {
		return err
	}
	for _, dv := range dictValues {
		if err := writeValue(w, innerType, dv); err != nil {
			return err
		}
	}

	if err := w.putInt64(int64(len(indices))); err != nil {
		return err
	}

	for _, idx := range indices {
		switch indexType {
		case 0:
			if err := w.putByte(byte(idx)); err != nil {
				return err
			}
		case 1:
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], uint16(idx))
			if _, err := w.Write(b[:]); err != nil {
				return err
			}
		default:
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], uint32(idx))
			if _, err := w.Write(b[:]); err != nil {
				return err
			}
		}
	}
	return nil
}
