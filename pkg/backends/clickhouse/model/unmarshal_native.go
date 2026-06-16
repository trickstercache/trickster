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
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

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
			// each column type header.
			if hasBlockInfo {
				if _, err := br.ReadByte(); err != nil {
					return nil, fmt.Errorf("native: column %d custom serialization flag: %w", c, err)
				}
			}

			vals := make([]string, numRows)
			for r := range numRows {
				vals[r], err = readValueAsString(br, typ)
				if err != nil {
					return nil, fmt.Errorf("native: column %d row %d: %w", c, r, err)
				}
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

func readFixed(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

//nolint:gosec // intentional wire protocol conversions
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
			v := int64(binary.LittleEndian.Uint64(b))
			return time.Unix(v/1000, (v%1000)*1000000).UTC().Format("2006-01-02 15:04:05.000"), nil
		}
		if strings.HasPrefix(typ, "DateTime(") {
			b, err := readFixed(r, 4)
			if err != nil {
				return "", err
			}
			ts := binary.LittleEndian.Uint32(b)
			return time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04:05"), nil
		}
		return readString(r)
	}
}
