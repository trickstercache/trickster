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
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// OutputFormatNative is the output format index for ClickHouse Native binary.
const OutputFormatNative byte = 6

// marshalTimeseriesNative writes a DataSet as a ClickHouse Native binary block.
func marshalTimeseriesNative(w io.Writer, ds *dataset.DataSet, rlo *timeseries.RequestOptions) error {
	// Collect all points across all series into row-major format
	type colDef struct {
		name string
		typ  string
	}

	if len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		return writeEmptyNativeBlock(w)
	}

	res := ds.Results[0]
	sh := res.SeriesList[0].Header

	// Build column definitions from the series header
	var cols []colDef
	cols = append(cols, colDef{name: sh.TimestampField.Name, typ: sh.TimestampField.SDataType})
	for _, tf := range sh.TagFieldsList {
		cols = append(cols, colDef{name: tf.Name, typ: tf.SDataType})
	}
	for _, vf := range sh.ValueFieldsList {
		cols = append(cols, colDef{name: vf.Name, typ: vf.SDataType})
	}

	// Collect all rows
	var rows [][]string
	for _, series := range res.SeriesList {
		for _, pt := range series.Points {
			row := make([]string, len(cols))
			// Timestamp
			row[0] = formatEpochForType(pt.Epoch, sh.TimestampField)
			// Tags
			for i, tf := range sh.TagFieldsList {
				row[1+i] = series.Header.Tags[tf.Name]
			}
			// Values
			for i, v := range pt.Values {
				row[1+len(sh.TagFieldsList)+i] = fmt.Sprint(v)
			}
			rows = append(rows, row)
		}
	}

	numCols := uint64(len(cols))
	numRows := uint64(len(rows))

	// Write TCP-style block info so clickhouse-go clients (including the
	// official Grafana plugin) can parse the response.
	if err := writeNativeBlockInfo(w); err != nil {
		return err
	}

	if err := writeUvarint(w, numCols); err != nil {
		return err
	}
	if err := writeUvarint(w, numRows); err != nil {
		return err
	}

	for c, col := range cols {
		if err := writeNativeString(w, col.name); err != nil {
			return err
		}
		if err := writeNativeString(w, col.typ); err != nil {
			return err
		}
		// customSerialization flag (matches TCP-style protocol)
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
		for r := range numRows {
			if err := writeNativeValue(w, col.typ, rows[r][c]); err != nil {
				return fmt.Errorf("marshal native col %d row %d: %w", c, r, err)
			}
		}
	}

	return nil
}

func writeEmptyNativeBlock(w io.Writer) error {
	if err := writeNativeBlockInfo(w); err != nil {
		return err
	}
	if err := writeUvarint(w, 0); err != nil {
		return err
	}
	return writeUvarint(w, 0)
}

func writeNativeBlockInfo(w io.Writer) error {
	// field 1: is_overflows = 0
	if err := writeUvarint(w, 1); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return err
	}
	// field 2: bucket_num = -1
	if err := writeUvarint(w, 2); err != nil {
		return err
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(0xFFFFFFFF)) //nolint:gosec
	if _, err := w.Write(b[:]); err != nil {
		return err
	}
	// end marker
	return writeUvarint(w, 0)
}

func writeUvarint(w io.Writer, v uint64) error {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	_, err := w.Write(buf[:n])
	return err
}

func writeNativeString(w io.Writer, s string) error {
	if err := writeUvarint(w, uint64(len(s))); err != nil {
		return err
	}
	if len(s) > 0 {
		_, err := w.Write([]byte(s))
		return err
	}
	return nil
}

//nolint:gosec // intentional wire protocol conversions
func writeNativeValue(w io.Writer, typ, val string) error {
	switch typ {
	case TypeUInt8, TypeBool:
		v, _ := strconv.ParseUint(val, 10, 8)
		_, err := w.Write([]byte{byte(v)})
		return err
	case TypeUInt16:
		v, _ := strconv.ParseUint(val, 10, 16)
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		_, err := w.Write(b[:])
		return err
	case TypeUInt32:
		v, _ := strconv.ParseUint(val, 10, 32)
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		_, err := w.Write(b[:])
		return err
	case TypeUInt64:
		v, _ := strconv.ParseUint(val, 10, 64)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], v)
		_, err := w.Write(b[:])
		return err
	case TypeInt8:
		v, _ := strconv.ParseInt(val, 10, 8)
		_, err := w.Write([]byte{byte(v)})
		return err
	case TypeInt16:
		v, _ := strconv.ParseInt(val, 10, 16)
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		_, err := w.Write(b[:])
		return err
	case TypeInt32:
		v, _ := strconv.ParseInt(val, 10, 32)
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		_, err := w.Write(b[:])
		return err
	case TypeInt64:
		v, _ := strconv.ParseInt(val, 10, 64)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(v))
		_, err := w.Write(b[:])
		return err
	case TypeFloat32:
		v, _ := strconv.ParseFloat(val, 32)
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(float32(v)))
		_, err := w.Write(b[:])
		return err
	case TypeFloat64:
		v, _ := strconv.ParseFloat(val, 64)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
		_, err := w.Write(b[:])
		return err
	case TypeDateTime:
		t, err := time.Parse(lsql.SQLDateTimeLayout, val)
		if err != nil {
			// Try as epoch seconds
			v, _ := strconv.ParseInt(val, 10, 64)
			t = time.Unix(v, 0)
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(t.Unix()))
		_, err = w.Write(b[:])
		return err
	case TypeDate:
		t, err := time.Parse("2006-01-02", val)
		if err != nil {
			return writeNativeString(w, val)
		}
		epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		days := uint16(t.Sub(epoch).Hours() / 24)
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], days)
		_, err = w.Write(b[:])
		return err
	case TypeString:
		return writeNativeString(w, val)
	default:
		// DateTime64 variants
		if strings.HasPrefix(typ, "DateTime64") {
			t, err := parseClickHouseTimestamp("", val)
			if err != nil {
				v, _ := strconv.ParseInt(val, 10, 64)
				var b [8]byte
				binary.LittleEndian.PutUint64(b[:], uint64(v))
				_, err = w.Write(b[:])
				return err
			}
			var b [8]byte
			binary.LittleEndian.PutUint64(b[:], uint64(t.UnixMilli()))
			_, err = w.Write(b[:])
			return err
		}
		return writeNativeString(w, val)
	}
}

func formatEpochForType(ep epoch.Epoch, tfd timeseries.FieldDefinition) string {
	nanos := int64(ep)
	t := time.Unix(nanos/1e9, nanos%1e9).UTC()
	switch tfd.SDataType {
	case TypeDateTime:
		return t.Format(lsql.SQLDateTimeLayout)
	case TypeDate:
		return t.Format("2006-01-02")
	default:
		if strings.HasPrefix(tfd.SDataType, "DateTime64") {
			return t.Format("2006-01-02 15:04:05.000")
		}
		// Default: epoch seconds as string
		return strconv.FormatInt(t.Unix(), 10)
	}
}
