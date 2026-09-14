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
	"io"
	"math"
	"math/big"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
)

func TestWriteUvarint(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 16383, 16384, 1 << 32, math.MaxUint64}
	for _, v := range cases {
		b := new(bytes.Buffer)
		if err := writeUvarint(b, v); err != nil {
			t.Fatalf("writeUvarint(%d): %v", v, err)
		}
		got, err := readUvarint(bufio.NewReader(bytes.NewReader(b.Bytes())))
		if err != nil {
			t.Fatalf("readUvarint for %d: %v", v, err)
		}
		if got != v {
			t.Fatalf("uvarint roundtrip: want %d got %d", v, got)
		}
	}
}

func TestWriteUvarint_WriterError(t *testing.T) {
	if err := writeUvarint(errWriter{}, 12345); err == nil {
		t.Fatal("expected error from failing writer")
	}
}

func TestWriteNativeString(t *testing.T) {
	cases := []string{"", "a", "hello world", strings.Repeat("x", 300)}
	for _, s := range cases {
		b := new(bytes.Buffer)
		if err := writeNativeString(b, s); err != nil {
			t.Fatalf("writeNativeString(%q): %v", s, err)
		}
		got, err := readString(bufio.NewReader(bytes.NewReader(b.Bytes())))
		if err != nil {
			t.Fatalf("readString: %v", err)
		}
		if got != s {
			t.Fatalf("string roundtrip: want %q got %q", s, got)
		}
	}
}

func TestWriteNativeString_WriterError(t *testing.T) {
	if err := writeNativeString(errWriter{}, "anything"); err == nil {
		t.Fatal("expected error writing length")
	}
	// empty string only writes the length; a writer that fails after first
	// byte still surfaces the error for non-empty payloads.
	if err := writeNativeString(shortWriter{max: 1}, "abc"); err == nil {
		t.Fatal("expected error writing body")
	}
}

func TestReadString_EmptyAndError(t *testing.T) {
	// length 0
	br := bufio.NewReader(bytes.NewReader([]byte{0}))
	s, err := readString(br)
	if err != nil || s != "" {
		t.Fatalf("empty string: %v %q", err, s)
	}
	// length 5 but only 3 bytes available
	br = bufio.NewReader(bytes.NewReader([]byte{5, 'a', 'b', 'c'}))
	if _, err := readString(br); err == nil {
		t.Fatal("expected short read error")
	}
	// uvarint read error
	br = bufio.NewReader(bytes.NewReader(nil))
	if _, err := readString(br); err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestReadUvarint_Error(t *testing.T) {
	br := bufio.NewReader(bytes.NewReader([]byte{0xff}))
	if _, err := readUvarint(br); err == nil {
		t.Fatal("expected error on truncated uvarint")
	}
}

func TestReadFixed(t *testing.T) {
	r := bytes.NewReader([]byte{1, 2, 3, 4, 5})
	b, err := readFixed(r, 3)
	if err != nil {
		t.Fatalf("readFixed: %v", err)
	}
	if !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("want [1 2 3] got %v", b)
	}
	if _, err := readFixed(r, 10); err == nil {
		t.Fatal("expected EOF")
	}
}

func TestWriteReadNativeValueRoundTrip(t *testing.T) {
	cases := []struct {
		typ, in, want string
	}{
		{TypeUInt8, "200", "200"},
		{TypeBool, "1", "1"},
		{TypeUInt16, "65535", "65535"},
		{TypeUInt32, "4294967295", "4294967295"},
		{TypeUInt64, "18446744073709551615", "18446744073709551615"},
		{TypeInt8, "-5", "-5"},
		{TypeInt16, "-20000", "-20000"},
		{TypeInt32, "-2147483648", "-2147483648"},
		{TypeInt64, "-9223372036854775808", "-9223372036854775808"},
		{TypeString, "hello", "hello"},
		{TypeString, "", ""},
		{TypeDateTime, "2020-01-01 00:00:00", "2020-01-01 00:00:00"},
		{TypeDateTime, "1577836800", "2020-01-01 00:00:00"},
		{TypeDate, "2020-01-01", "2020-01-01"},
		{"Enum8('orange' = 1, 'blue' = 2, 'purple' = 3)", "purple", "purple"},
		{"Enum8('neg' = -1, 'zero' = 0)", "neg", "neg"},
		{"Enum16('a' = 300, 'b\\'c' = -2)", "b'c", "b'c"},
		{"FixedString(4)", "ab", "ab"},
		{"FixedString(4)", "abcd", "abcd"},
	}
	for _, c := range cases {
		b := new(bytes.Buffer)
		if err := writeNativeValue(b, c.typ, c.in); err != nil {
			t.Fatalf("writeNativeValue(%s,%q): %v", c.typ, c.in, err)
		}
		br := bufio.NewReader(bytes.NewReader(b.Bytes()))
		got, err := readValueAsString(br, c.typ)
		if err != nil {
			t.Fatalf("readValueAsString(%s): %v", c.typ, err)
		}
		if got != c.want {
			t.Fatalf("%s: want %q got %q", c.typ, c.want, got)
		}
	}
}

func TestWriteReadFloatValues(t *testing.T) {
	// floats don't exactly roundtrip string<->bits; verify the numeric value.
	b := new(bytes.Buffer)
	if err := writeNativeValue(b, TypeFloat32, "3.5"); err != nil {
		t.Fatal(err)
	}
	got, err := readValueAsString(bufio.NewReader(bytes.NewReader(b.Bytes())), TypeFloat32)
	if err != nil {
		t.Fatal(err)
	}
	if got != "3.5" {
		t.Fatalf("float32: want 3.5 got %q", got)
	}

	b.Reset()
	if err := writeNativeValue(b, TypeFloat64, "1.25"); err != nil {
		t.Fatal(err)
	}
	got, err = readValueAsString(bufio.NewReader(bytes.NewReader(b.Bytes())), TypeFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.25" {
		t.Fatalf("float64: want 1.25 got %q", got)
	}
}

func TestWriteNativeValue_DateTime64(t *testing.T) {
	b := new(bytes.Buffer)
	if err := writeNativeValue(b, "DateTime64(3)", "2020-01-01 00:00:00.000"); err != nil {
		t.Fatal(err)
	}
	got, err := readValueAsString(bufio.NewReader(bytes.NewReader(b.Bytes())), "DateTime64(3)")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2020-01-01 00:00:00.000" {
		t.Fatalf("DateTime64 roundtrip: got %q", got)
	}

	// epoch-ms fallback path: unparsable text falls through to ParseInt
	b.Reset()
	if err := writeNativeValue(b, "DateTime64(3)", "1577836800000"); err != nil {
		t.Fatal(err)
	}
	got, err = readValueAsString(bufio.NewReader(bytes.NewReader(b.Bytes())), "DateTime64(3)")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2020-01-01 00:00:00.000" {
		t.Fatalf("DateTime64 epoch-ms: got %q", got)
	}
}

func TestReadValueAsString_DateTime64Precision(t *testing.T) {
	for _, test := range []struct {
		typ   string
		ticks int64
		want  string
	}{
		{"DateTime64(0)", 1577836800, "2020-01-01 00:00:00"},
		{"DateTime64(3)", 1577836800123, "2020-01-01 00:00:00.123"},
		{"DateTime64(6)", 1577836800123456, "2020-01-01 00:00:00.123456"},
		{"DateTime64(9, 'UTC')", 1577836800123456789, "2020-01-01 00:00:00.123456789"},
		{"DateTime64(6)", -1, "1969-12-31 23:59:59.999999"},
	} {
		t.Run(test.typ+"_"+strconv.FormatInt(test.ticks, 10), func(t *testing.T) {
			var data [8]byte
			binary.LittleEndian.PutUint64(data[:], uint64(test.ticks))
			got, err := readValueAsString(bufio.NewReader(bytes.NewReader(data[:])), test.typ)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
	var data [8]byte
	if _, err := readValueAsString(bufio.NewReader(bytes.NewReader(data[:])), "DateTime64(10)"); err == nil {
		t.Fatal("accepted invalid DateTime64 precision")
	}
}

func TestWriteNativeValue_UnknownTypeAsString(t *testing.T) {
	b := new(bytes.Buffer)
	if err := writeNativeValue(b, "SomeUnknownType", "payload"); err != nil {
		t.Fatal(err)
	}
	got, err := readValueAsString(bufio.NewReader(bytes.NewReader(b.Bytes())), "SomeUnknownType")
	if err != nil {
		t.Fatal(err)
	}
	if got != "payload" {
		t.Fatalf("unknown type: got %q", got)
	}
}

func TestWriteNativeValue_DateUnparsable(t *testing.T) {
	// Unparsable Date falls through to writeNativeString
	b := new(bytes.Buffer)
	if err := writeNativeValue(b, TypeDate, "not-a-date"); err != nil {
		t.Fatal(err)
	}
	// reading as Date consumes 2 bytes — here the first byte is the uvarint
	// length written by the string fallback. Just verify bytes were written.
	if b.Len() == 0 {
		t.Fatal("expected bytes written on Date fallback")
	}
}

func TestReadValueAsString_DateTimeWithTimezone(t *testing.T) {
	// DateTime('UTC') is a 4-byte UInt32 on the ClickHouse native wire;
	// the timezone is metadata only. Reading 8 bytes (as DateTime64 would)
	// desynchronizes the column stream and corrupts every subsequent value.
	for _, typ := range []string{
		"DateTime('UTC')",
		"DateTime('Asia/Tokyo')",
		"DateTime('America/New_York')",
	} {
		b := new(bytes.Buffer)
		// 1577836800 = 2020-01-01 00:00:00 UTC, written as 4-byte LE UInt32.
		_ = binary.Write(b, binary.LittleEndian, uint32(1577836800))
		// Sentinel UInt32 so we can detect over-read.
		_ = binary.Write(b, binary.LittleEndian, uint32(0xDEADBEEF))

		br := bufio.NewReader(bytes.NewReader(b.Bytes()))
		got, err := readValueAsString(br, typ)
		if err != nil {
			t.Fatalf("%s: readValueAsString: %v", typ, err)
		}
		if got != "2020-01-01 00:00:00" {
			t.Fatalf("%s: want 2020-01-01 00:00:00, got %q", typ, got)
		}
		// If readValueAsString consumed more than 4 bytes, the sentinel is lost.
		next, err := readFixed(br, 4)
		if err != nil {
			t.Fatalf("%s: sentinel read: %v", typ, err)
		}
		if binary.LittleEndian.Uint32(next) != 0xDEADBEEF {
			t.Fatalf("%s: sentinel mismatch: got %#x", typ, binary.LittleEndian.Uint32(next))
		}
	}
}

func TestReadValueAsString_ShortBuffers(t *testing.T) {
	types := []string{
		TypeUInt16, TypeUInt32, TypeUInt64, TypeInt16,
		TypeInt32, TypeInt64, TypeFloat32, TypeFloat64, TypeDateTime, TypeDate,
		"DateTime64(3)",
	}
	for _, typ := range types {
		br := bufio.NewReader(bytes.NewReader(nil))
		if _, err := readValueAsString(br, typ); err == nil {
			t.Fatalf("%s: expected error on empty reader", typ)
		}
	}
}

func TestReadValueAsString_Int8UInt8Empty(t *testing.T) {
	// UInt8/Bool/Int8 call ReadByte which surfaces io.EOF.
	for _, typ := range []string{TypeUInt8, TypeInt8, TypeBool} {
		br := bufio.NewReader(bytes.NewReader(nil))
		if _, err := readValueAsString(br, typ); err == nil {
			t.Fatalf("%s: expected EOF", typ)
		}
	}
}

func TestFormatEpochForType(t *testing.T) {
	ep := epoch.Epoch(int64(1577836800) * 1e9) // 2020-01-01 00:00:00 UTC
	cases := []struct {
		sdt, want string
	}{
		{TypeDateTime, "2020-01-01 00:00:00"},
		{TypeDate, "2020-01-01"},
		{"DateTime64(3)", "2020-01-01 00:00:00.000"},
		{"UInt64", "1577836800"},
	}
	for _, c := range cases {
		got := formatEpochForType(ep, timeseries.FieldDefinition{SDataType: c.sdt})
		if got != c.want {
			t.Fatalf("%s: want %q got %q", c.sdt, c.want, got)
		}
	}
}

func TestWriteNativeBlockInfo(t *testing.T) {
	b := new(bytes.Buffer)
	if err := writeNativeBlockInfo(b); err != nil {
		t.Fatal(err)
	}
	if err := skipBlockInfo(bufio.NewReader(bytes.NewReader(b.Bytes()))); err != nil {
		t.Fatalf("skipBlockInfo: %v", err)
	}
}

func TestWriteEmptyNativeBlock(t *testing.T) {
	b := new(bytes.Buffer)
	if err := writeEmptyNativeBlock(b); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(bytes.NewReader(b.Bytes()))
	if err := skipBlockInfo(br); err != nil {
		t.Fatal(err)
	}
	ncols, err := readUvarint(br)
	if err != nil || ncols != 0 {
		t.Fatalf("numCols: %d err=%v", ncols, err)
	}
	nrows, err := readUvarint(br)
	if err != nil || nrows != 0 {
		t.Fatalf("numRows: %d err=%v", nrows, err)
	}
}

func TestSkipBlockInfo_UnknownField(t *testing.T) {
	// fieldNum=7 is unknown, handler returns error.
	buf := &bytes.Buffer{}
	_ = writeUvarint(buf, 7)
	if err := skipBlockInfo(bufio.NewReader(bytes.NewReader(buf.Bytes()))); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestSkipBlockInfo_ErrorOnRead(t *testing.T) {
	// truncated field-1 payload (need 1 byte after fieldNum)
	buf := &bytes.Buffer{}
	_ = writeUvarint(buf, 1)
	if err := skipBlockInfo(bufio.NewReader(bytes.NewReader(buf.Bytes()))); err == nil {
		t.Fatal("expected read error on field 1 body")
	}
	// truncated field-2 payload (need 4 bytes)
	buf.Reset()
	_ = writeUvarint(buf, 2)
	if err := skipBlockInfo(bufio.NewReader(bytes.NewReader(buf.Bytes()))); err == nil {
		t.Fatal("expected read error on field 2 body")
	}
	// no fieldNum at all
	if err := skipBlockInfo(bufio.NewReader(bytes.NewReader(nil))); err == nil {
		t.Fatal("expected error reading fieldNum")
	}
}

func TestMarshalTimeseriesNative_RoundTrip(t *testing.T) {
	ds := testDataSet()
	b := new(bytes.Buffer)
	if err := marshalTimeseriesNative(b, ds, &timeseries.RequestOptions{}); err != nil {
		t.Fatalf("marshal: %v", err)
	}

	ts, err := UnmarshalTimeseriesNative(b.Bytes(), testTRQ.Clone())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := ts.(*dataset.DataSet)
	if !ok || got == nil {
		t.Fatal("expected non-nil DataSet")
	}
	if len(got.Results) != 1 || len(got.Results[0].SeriesList) != 1 {
		t.Fatalf("unexpected shape: results=%d", len(got.Results))
	}
	gotSeries := got.Results[0].SeriesList[0]
	wantSeries := ds.Results[0].SeriesList[0]
	if len(gotSeries.Points) != len(wantSeries.Points) {
		t.Fatalf("points: want %d got %d", len(wantSeries.Points), len(gotSeries.Points))
	}
	for i := range wantSeries.Points {
		if gotSeries.Points[i].Epoch != wantSeries.Points[i].Epoch {
			t.Errorf("point %d epoch: want %d got %d", i,
				wantSeries.Points[i].Epoch, gotSeries.Points[i].Epoch)
		}
	}
}

func TestMarshalTimeseriesNative_EmptyDataSet(t *testing.T) {
	b := new(bytes.Buffer)
	ds := &dataset.DataSet{}
	if err := marshalTimeseriesNative(b, ds, &timeseries.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	// empty block — UnmarshalTimeseriesNative should see no rows and return
	// ErrInvalidBody.
	if _, err := UnmarshalTimeseriesNative(b.Bytes(), testTRQ.Clone()); err == nil {
		t.Fatal("expected error on empty block")
	}
}

func TestMarshalTimeseriesNative_WriterError(t *testing.T) {
	ds := testDataSet()
	for i := range 40 {
		err := marshalTimeseriesNative(shortWriter{max: i}, ds, &timeseries.RequestOptions{})
		if err == nil {
			// Once the short writer accepts enough bytes it should succeed.
			// Just ensure small limits produce errors.
			if i < 5 {
				t.Fatalf("limit=%d: expected error", i)
			}
		}
	}
}

func TestUnmarshalTimeseriesNative_NoBlockInfo(t *testing.T) {
	// Build a minimal block without block-info header. First byte (numCols
	// uvarint) must NOT be 1 so the peek branch in the reader skips
	// skipBlockInfo. Use 2 cols, 1 row.
	b := new(bytes.Buffer)
	_ = writeUvarint(b, 2) // numCols
	_ = writeUvarint(b, 1) // numRows
	_ = writeNativeString(b, "t")
	_ = writeNativeString(b, "UInt64")
	// no customSerialization byte (since no block info)
	_ = writeNativeValue(b, "UInt64", "1577836800000")
	_ = writeNativeString(b, "hostname")
	_ = writeNativeString(b, "String")
	_ = writeNativeValue(b, "String", "localhost")

	ts, err := UnmarshalTimeseriesNative(b.Bytes(), testTRQ.Clone())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		t.Fatal("expected non-nil DataSet")
	}
}

func TestUnmarshalTimeseriesNative_EmptyReaderError(t *testing.T) {
	if _, err := UnmarshalTimeseriesNative(nil, testTRQ.Clone()); err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestUnmarshalTimeseriesNative_ColumnNameReadError(t *testing.T) {
	b := new(bytes.Buffer)
	_ = writeNativeBlockInfo(b)
	_ = writeUvarint(b, 2) // numCols
	_ = writeUvarint(b, 1) // numRows
	// truncate here: reader expects a column name but hits EOF
	if _, err := UnmarshalTimeseriesNativeReader(bytes.NewReader(b.Bytes()), testTRQ.Clone()); err == nil {
		t.Fatal("expected error on truncated column name")
	}
}

func TestUnmarshalTimeseriesNative_ColumnTypeReadError(t *testing.T) {
	b := new(bytes.Buffer)
	_ = writeNativeBlockInfo(b)
	_ = writeUvarint(b, 1) // numCols
	_ = writeUvarint(b, 1) // numRows
	_ = writeNativeString(b, "t")
	// truncate before type
	if _, err := UnmarshalTimeseriesNativeReader(bytes.NewReader(b.Bytes()), testTRQ.Clone()); err == nil {
		t.Fatal("expected error on truncated column type")
	}
}

func TestUnmarshalTimeseriesNative_ValueReadError(t *testing.T) {
	b := new(bytes.Buffer)
	_ = writeNativeBlockInfo(b)
	_ = writeUvarint(b, 1) // numCols
	_ = writeUvarint(b, 1) // numRows
	_ = writeNativeString(b, "t")
	_ = writeNativeString(b, "UInt64")
	_, _ = b.Write([]byte{0}) // customSerialization flag
	// truncate — UInt64 needs 8 bytes
	if _, err := UnmarshalTimeseriesNativeReader(bytes.NewReader(b.Bytes()), testTRQ.Clone()); err == nil {
		t.Fatal("expected error on truncated value")
	}
}

func TestUnmarshalTimeseriesNative_CustomSerializationEOF(t *testing.T) {
	b := new(bytes.Buffer)
	_ = writeNativeBlockInfo(b)
	_ = writeUvarint(b, 1) // numCols
	_ = writeUvarint(b, 1) // numRows
	_ = writeNativeString(b, "t")
	_ = writeNativeString(b, "UInt64")
	// missing customSerialization flag byte
	if _, err := UnmarshalTimeseriesNativeReader(bytes.NewReader(b.Bytes()), testTRQ.Clone()); err == nil {
		t.Fatal("expected error on missing custom serialization byte")
	}
}

func TestUnmarshalTimeseriesNativeReader_MultiBlock(t *testing.T) {
	// concatenate two blocks — unmarshal should aggregate rows from both.
	b := new(bytes.Buffer)
	writeBlock := func(rows int) {
		_ = writeNativeBlockInfo(b)
		_ = writeUvarint(b, 2)            // numCols
		_ = writeUvarint(b, uint64(rows)) // numRows
		_ = writeNativeString(b, "t")
		_ = writeNativeString(b, "UInt64")
		_, _ = b.Write([]byte{0})
		for i := range rows {
			_ = writeNativeValue(b, "UInt64",
				strconv.FormatInt(1577836800000+int64(i)*60000, 10))
		}
		_ = writeNativeString(b, "hostname")
		_ = writeNativeString(b, "String")
		_, _ = b.Write([]byte{0})
		for range rows {
			_ = writeNativeValue(b, "String", "localhost")
		}
	}
	writeBlock(2)
	writeBlock(1)

	ts, err := UnmarshalTimeseriesNativeReader(bytes.NewReader(b.Bytes()), testTRQ.Clone())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ds := ts.(*dataset.DataSet)
	total := 0
	for _, s := range ds.Results[0].SeriesList {
		total += len(s.Points)
	}
	if total != 3 {
		t.Fatalf("expected 3 total points across blocks, got %d", total)
	}
}

// Assemble a block with a terminating zero-cols block (as ClickHouse sends).
func TestUnmarshalTimeseriesNativeReader_ZeroColsTerminator(t *testing.T) {
	b := new(bytes.Buffer)
	_ = writeNativeBlockInfo(b)
	_ = writeUvarint(b, 2)
	_ = writeUvarint(b, 1)
	_ = writeNativeString(b, "t")
	_ = writeNativeString(b, "UInt64")
	_, _ = b.Write([]byte{0})
	_ = writeNativeValue(b, "UInt64", "1577836800000")
	_ = writeNativeString(b, "hostname")
	_ = writeNativeString(b, "String")
	_, _ = b.Write([]byte{0})
	_ = writeNativeValue(b, "String", "localhost")
	// terminator block: info + 0 cols + 0 rows
	_ = writeNativeBlockInfo(b)
	_ = writeUvarint(b, 0)
	_ = writeUvarint(b, 0)

	ts, err := UnmarshalTimeseriesNativeReader(bytes.NewReader(b.Bytes()), testTRQ.Clone())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ts == nil {
		t.Fatal("nil timeseries")
	}
}

func TestNopReaderBehavior(t *testing.T) {
	r := &nopReader{data: []byte("world"), pos: 0}
	buf := make([]byte, 3)
	n, err := r.Read(buf)
	if err != nil || n != 3 || string(buf) != "wor" {
		t.Fatalf("read1: n=%d err=%v buf=%q", n, err, buf)
	}
	n, err = r.Read(buf)
	if err != nil || n != 2 || string(buf[:n]) != "ld" {
		t.Fatalf("read2: n=%d err=%v buf=%q", n, err, buf[:n])
	}
	n, err = r.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read3 want EOF: n=%d err=%v", n, err)
	}
}

// Cover via the higher-level MarshalTimeseries dispatch for OutputFormatNative.
func TestMarshalTimeseries_NativeFormat(t *testing.T) {
	out, err := MarshalTimeseries(testDataSet(),
		&timeseries.RequestOptions{OutputFormat: OutputFormatNative}, 200)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty output")
	}
	// Verify it parses back.
	if _, err := UnmarshalTimeseriesNative(out, testTRQ.Clone()); err != nil {
		t.Fatalf("roundtrip unmarshal: %v", err)
	}
}

// --- test helpers ---

type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("boom") }

type shortWriter struct {
	max, written int
}

func (s shortWriter) Write(p []byte) (int, error) {
	remaining := s.max - s.written
	if remaining <= 0 {
		return 0, errors.New("write limit exceeded")
	}
	n := len(p)
	if n > remaining {
		return remaining, errors.New("write limit exceeded")
	}
	return n, nil
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
		t, err := time.Parse(timeconv.SQLDateTimeLayout, val)
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
		if strings.HasPrefix(typ, "Enum8(") || strings.HasPrefix(typ, "Enum16(") {
			index := 0
			for i, name := range parseEnumMembers(typ) {
				if name == val {
					index = i
				}
			}
			if strings.HasPrefix(typ, "Enum8(") {
				_, err := w.Write([]byte{byte(int8(index))})
				return err
			}
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], uint16(int16(index)))
			_, err := w.Write(b[:])
			return err
		}
		if after, ok := strings.CutPrefix(typ, "FixedString("); ok {
			n, _ := strconv.Atoi(strings.TrimSuffix(after, ")"))
			b := make([]byte, n)
			copy(b, val)
			_, err := w.Write(b)
			return err
		}
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

func TestReadEnumValue_UnknownIndexAndErrors(t *testing.T) {
	typ := "Enum8('a' = 1)"
	got, err := readValueAsString(bufio.NewReader(bytes.NewReader([]byte{7})), typ)
	if err != nil || got != "7" {
		t.Fatalf("unknown index: got %q, %v", got, err)
	}
	if _, err := readValueAsString(bufio.NewReader(bytes.NewReader(nil)), typ); err == nil {
		t.Fatal("expected read error for empty Enum8")
	}
	if _, err := readValueAsString(bufio.NewReader(bytes.NewReader([]byte{1})), "Enum16('a' = 1)"); err == nil {
		t.Fatal("expected read error for short Enum16")
	}
	if _, err := readValueAsString(bufio.NewReader(bytes.NewReader([]byte{1})), "FixedString(x)"); err == nil {
		t.Fatal("expected error for invalid FixedString length")
	}
	if _, err := readValueAsString(bufio.NewReader(bytes.NewReader([]byte{1})), "FixedString(4)"); err == nil {
		t.Fatal("expected read error for short FixedString")
	}
	members := parseEnumMembers("Enum8('x' = 1, 'y', 'z' = 3)")
	if members[1] != "x" || members[3] != "z" || len(members) != 2 {
		t.Fatalf("unexpected members %v", members)
	}
	if got := parseEnumMembers("Enum8()"); len(got) != 0 {
		t.Fatalf("expected no members, got %v", got)
	}
}

// encodeWithClickHouseGo produces the wire bytes for one column of values
// using the official client, so the reader is checked against the real
// layout (byte order, null maps) rather than against this package's writer.
func encodeWithClickHouseGo(t *testing.T, typ string, values ...any) *bufio.Reader {
	t.Helper()
	col, err := column.Type(typ).Column("x", &column.ServerContext{Revision: server.ServerRevision, Timezone: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if err := col.AppendRow(v); err != nil {
			t.Fatalf("%s: append %v: %v", typ, v, err)
		}
	}
	var buf proto.Buffer
	col.Encode(&buf)
	return bufio.NewReader(bytes.NewReader(buf.Buf))
}

func bigInt(text string) *big.Int {
	v, _ := new(big.Int).SetString(text, 10)
	return v
}

func TestReadValueAsString_ScalarsFromClickHouseGo(t *testing.T) {
	for _, c := range []struct {
		typ  string
		in   any
		want string
	}{
		{"Date32", time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), "1969-12-31"},
		{"Date32", time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), "2024-02-29"},
		{"Int128", bigInt("-170141183460469231731687303715884105728"), "-170141183460469231731687303715884105728"},
		{"UInt128", bigInt("340282366920938463463374607431768211455"), "340282366920938463463374607431768211455"},
		{"Int256", bigInt("-42"), "-42"},
		{"UInt256", bigInt("115792089237316195423570985008687907853269984665640564039457584007913129639935"), "115792089237316195423570985008687907853269984665640564039457584007913129639935"},
		{"UUID", "61f0c404-5cb3-11e7-907b-a6006ad3dba0", "61f0c404-5cb3-11e7-907b-a6006ad3dba0"},
		{"IPv4", net.ParseIP("10.1.2.3"), "10.1.2.3"},
		{"IPv6", net.ParseIP("2001:db8::1"), "2001:db8::1"},
		{"Enum8('orange' = 1, 'blue' = 2)", "blue", "blue"},
		{"Enum16('x' = -300)", "x", "x"},
		{"FixedString(6)", "abc", "abc"},
		{"Bool", true, "1"},
	} {
		got, err := readValueAsString(encodeWithClickHouseGo(t, c.typ, c.in), c.typ)
		if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.typ, got, c.want)
		}
	}
	// decimals are scaled two's-complement integers of 4/8/16/32 bytes
	for _, c := range []struct {
		typ, scaled, want string
	}{
		{"Decimal(9, 2)", "-1483", "-14.83"},
		{"Decimal(18, 4)", "1", "0.0001"},
		{"Decimal(38, 3)", "123456789012345678901234567", "123456789012345678901234.567"},
		{"Decimal(76, 0)", "7", "7"},
		{"Decimal(10, 3)", "12500", "12.500"},
		{"Decimal(30, 2)", "-5", "-0.05"},
		{"Decimal32(2)", "1477", "14.77"},
		{"Decimal64(2)", "1477", "14.77"},
		{"Decimal128(2)", "1477", "14.77"},
		{"Decimal256(2)", "1477", "14.77"},
	} {
		got, err := readValueAsString(bufio.NewReader(bytes.NewReader(scaledDecimalBytes(c.typ, c.scaled))), c.typ)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.typ, got, err, c.want)
		}
	}
}

// scaledDecimalBytes writes a decimal's scaled integer in ClickHouse's
// little-endian two's-complement wire form for the type's width.
func scaledDecimalBytes(typ, scaled string) []byte {
	width := map[byte]int{'3': 4, '6': 8, '1': 16, '2': 32}[typ[len("Decimal")]] // Decimal32/64/128/256
	if strings.HasPrefix(typ, "Decimal(") {
		precision, _ := strconv.Atoi(strings.TrimSpace(strings.Split(typ[len("Decimal("):], ",")[0]))
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
	}
	v, _ := new(big.Int).SetString(scaled, 10)
	if v.Sign() < 0 {
		v.Add(v, new(big.Int).Lsh(big.NewInt(1), uint(8*width)))
	}
	be := v.FillBytes(make([]byte, width))
	le := make([]byte, width)
	for i := range be {
		le[i] = be[width-1-i]
	}
	return le
}

func TestReadColumnValues_Nullable(t *testing.T) {
	r := encodeWithClickHouseGo(t, "Nullable(Float64)", 1.5, nil, 2.25)
	got, err := readColumnValues(r, "Nullable(Float64)", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.5", nullToken, "2.25"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	r = encodeWithClickHouseGo(t, "Nullable(String)", nil, "a")
	got, err = readColumnValues(r, "Nullable(String)", 2)
	if err != nil || got[0] != nullToken || got[1] != "a" {
		t.Fatalf("Nullable(String): %v %v", got, err)
	}
	if _, err := readColumnValues(bufio.NewReader(bytes.NewReader([]byte{0})), "Nullable(UInt8)", 2); err == nil {
		t.Fatal("expected short null map error")
	}
	if _, err := readColumnValues(bufio.NewReader(bytes.NewReader([]byte{0, 0, 1})), "Nullable(UInt16)", 2); err == nil {
		t.Fatal("expected short value error")
	}
}

func TestReadColumnValues_RejectsCompoundTypes(t *testing.T) {
	for _, typ := range []string{"Nested(a UInt8)", "Variant(UInt8, String)", "Dynamic", "JSON", "Array(Variant(UInt8))"} {
		_, err := readColumnValues(bufio.NewReader(bytes.NewReader(make([]byte, 64))), typ, 1)
		if !errors.Is(err, errUnsupportedColumnType) {
			t.Errorf("%s: got %v, want unsupported column type", typ, err)
		}
	}
}

func TestUnmarshalNative_RejectsCustomSerialization(t *testing.T) {
	b := new(bytes.Buffer)
	if err := writeNativeBlockInfo(b); err != nil {
		t.Fatal(err)
	}
	_ = writeUvarint(b, 1) // columns
	_ = writeUvarint(b, 1) // rows
	_ = writeNativeString(b, "c")
	_ = writeNativeString(b, "String")
	b.WriteByte(1) // custom serialization flag set
	_, err := UnmarshalTimeseriesNative(b.Bytes(), nil)
	if !errors.Is(err, errUnsupportedSerialization) {
		t.Fatalf("got %v, want unsupported serialization", err)
	}
}

func TestReadDecimalValue_Invalid(t *testing.T) {
	for _, typ := range []string{"Decimal(x, 2)", "Decimal64(-1)", "Decimal64(a)", "Decimal(10)", "DecimalFoo(1)"} {
		if _, err := readValueAsString(bufio.NewReader(bytes.NewReader(make([]byte, 32))), typ); err == nil {
			t.Errorf("%s: expected error", typ)
		}
	}
	if _, err := readValueAsString(bufio.NewReader(bytes.NewReader([]byte{1})), "Decimal64(2)"); err == nil {
		t.Error("expected short read error")
	}
	for _, typ := range []string{"Date32", "Int128", "UUID", "IPv4", "IPv6"} {
		if _, err := readValueAsString(bufio.NewReader(bytes.NewReader([]byte{1})), typ); err == nil {
			t.Errorf("%s: expected short read error", typ)
		}
	}
}

// TestNativeNullableTagRoundTrip feeds an origin block with a NULL tag value
// through the reader and the marshaller, and checks the NULL survives as a
// real null in the re-encoded block rather than as literal text.
func TestNativeNullableTagRoundTrip(t *testing.T) {
	block := new(bytes.Buffer)
	_ = writeUvarint(block, 3) // columns
	_ = writeUvarint(block, 2) // rows
	for _, col := range []struct {
		name, typ string
		values    []any
	}{
		{"t", "DateTime", []any{time.Unix(1700000000, 0).UTC(), time.Unix(1700000060, 0).UTC()}},
		{"cab", "Nullable(String)", []any{"blue", nil}},
		{"v", "Nullable(Float64)", []any{1.5, nil}},
	} {
		_ = writeNativeString(block, col.name)
		_ = writeNativeString(block, col.typ)
		raw, _ := io.ReadAll(encodeWithClickHouseGo(t, col.typ, col.values...))
		block.Write(raw)
	}
	trq := testTRQ.Clone()
	ts, err := UnmarshalTimeseriesNative(block.Bytes(), trq)
	if err != nil {
		t.Fatal(err)
	}
	out := new(bytes.Buffer)
	if err := marshalTimeseriesNative(out, ts.(*dataset.DataSet), &timeseries.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	// re-read the marshalled block: the null map must mark the NULL tag row
	br := bufio.NewReader(bytes.NewReader(out.Bytes()))
	if peek, _ := br.Peek(1); peek[0] == 1 {
		if err := skipBlockInfo(br); err != nil {
			t.Fatal(err)
		}
	}
	numCols, _ := readUvarint(br)
	numRows, _ := readUvarint(br)
	seen := map[string][]string{}
	for range numCols {
		name, _ := readString(br)
		typ, _ := readString(br)
		if _, err := br.ReadByte(); err != nil { // custom serialization flag
			t.Fatal(err)
		}
		vals, err := readColumnValues(br, typ, numRows)
		if err != nil {
			t.Fatalf("%s (%s): %v", name, typ, err)
		}
		seen[name] = vals
	}
	if !slices.Contains(seen["cab"], nullToken) || !slices.Contains(seen["cab"], "blue") {
		t.Fatalf("nullable tag not preserved: %v", seen["cab"])
	}
	if !slices.Contains(seen["v"], nullToken) || !slices.Contains(seen["v"], "1.5") {
		t.Fatalf("nullable value not preserved: %v", seen["v"])
	}
}

// encodeColumnWithPrefix is encodeWithClickHouseGo plus the serialization
// state prefix that LowCardinality columns carry in Native blocks.
func encodeColumnWithPrefix(t *testing.T, typ string, values ...any) *bufio.Reader {
	t.Helper()
	col, err := column.Type(typ).Column("x", &column.ServerContext{Revision: server.ServerRevision, Timezone: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if err := col.AppendRow(v); err != nil {
			t.Fatalf("%s: append %v: %v", typ, v, err)
		}
	}
	var buf proto.Buffer
	if custom, ok := col.(column.CustomSerialization); ok {
		if err := custom.WriteStatePrefix(&buf); err != nil {
			t.Fatal(err)
		}
	}
	col.Encode(&buf)
	return bufio.NewReader(bytes.NewReader(buf.Buf))
}

func TestReadColumnValues_CompoundTypes(t *testing.T) {
	for _, c := range []struct {
		typ    string
		in     []any
		want   []string
		prefix bool
	}{
		{"Array(UInt8)", []any{[]uint8{1, 2}, []uint8{}, []uint8{3}}, []string{"[1,2]", "[]", "[3]"}, false},
		{"Array(String)", []any{[]string{"a", "b'c", "x\\y"}}, []string{`['a','b\'c','x\\y']`}, false},
		{"Array(Array(UInt16))", []any{[][]uint16{{1}, {2, 3}}}, []string{"[[1],[2,3]]"}, false},
		{"Array(Nullable(UInt8))", []any{[]*uint8{new(uint8(1)), nil}}, []string{"[1,NULL]"}, false},
		{"Array(Bool)", []any{[]bool{true, false}}, []string{"[true,false]"}, false},
		{"Map(String, UInt64)", []any{map[string]uint64{"a": 1}, map[string]uint64{}}, []string{"{'a':1}", "{}"}, false},
		{"Map(LowCardinality(String), LowCardinality(String))", []any{map[string]string{"k": "v"}}, []string{"{'k':'v'}"}, true},
		{"Tuple(a LowCardinality(String), b UInt8)", []any{[]any{"x", uint8(1)}}, []string{"('x',1)"}, true},
		{"Tuple(String, UInt8)", []any{[]any{"x", uint8(2)}, []any{"y", uint8(3)}}, []string{"('x',2)", "('y',3)"}, false},
		{"Tuple(name String, n UInt8)", []any{[]any{"x", uint8(2)}}, []string{"('x',2)"}, false},
		{"LowCardinality(String)", []any{"orange", "blue", "orange"}, []string{"orange", "blue", "orange"}, true},
		{"LowCardinality(Nullable(String))", []any{"a", nil, "a"}, []string{"a", nullToken, "a"}, true},
		{"Array(LowCardinality(String))", []any{[]string{"p", "q"}}, []string{"['p','q']"}, true},
	} {
		var r *bufio.Reader
		if c.prefix {
			r = encodeColumnWithPrefix(t, c.typ, c.in...)
		} else {
			r = encodeWithClickHouseGo(t, c.typ, c.in...)
		}
		got, err := readColumn(r, c.typ, uint64(len(c.in)))
		if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.typ, got, c.want)
		}
		if rest, _ := io.ReadAll(r); len(rest) != 0 {
			t.Errorf("%s: %d bytes left unread", c.typ, len(rest))
		}
	}
	if _, err := readColumn(bufio.NewReader(bytes.NewReader(make([]byte, 24))), "LowCardinality(String)", 1); err == nil {
		t.Error("expected key version error")
	}
	if _, err := readColumn(bufio.NewReader(bytes.NewReader([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 1})), "Tuple(a LowCardinality(String), b UInt8)", 1); err == nil {
		t.Error("expected error for a short nested LowCardinality column")
	}
	if _, err := readColumnValues(bufio.NewReader(bytes.NewReader([]byte{2, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0})), "Array(UInt8)", 2); err == nil {
		t.Error("expected invalid offsets error")
	}
	if _, err := readColumnValues(bufio.NewReader(bytes.NewReader(nil)), "Map(String)", 1); err == nil {
		t.Error("expected Map arity error")
	}
	if got := splitTypeList("Map(String, UInt8), Tuple(a String, b Enum8('x,y' = 1))"); len(got) != 2 {
		t.Errorf("splitTypeList: %v", got)
	}
}

//go:fix inline
func ptrTo[T any](v T) *T { return new(v) }

// TestCompoundNativeRoundTrip proves the two representations meet: values
// decoded from an origin Native block re-encode to identical column bytes.
func TestCompoundNativeRoundTrip(t *testing.T) {
	for _, c := range []struct {
		typ string
		in  []any
	}{
		{"Array(String)", []any{[]string{"a", "b'c"}, []string{}}},
		{"Map(String, UInt64)", []any{map[string]uint64{"k": 9}}},
		{"Tuple(String, UInt8)", []any{[]any{"x", uint8(2)}}},
		{"Array(Array(UInt16))", []any{[][]uint16{{1}, {2, 3}}}},
	} {
		text, err := readColumnValues(encodeWithClickHouseGo(t, c.typ, c.in...), c.typ, uint64(len(c.in)))
		if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		vals := make([]any, len(text))
		for i := range text {
			vals[i] = text[i]
		}
		var out bytes.Buffer
		if err := server.EncodeNativeBlock(&out, []server.Column{{Name: "x", Type: c.typ}}, [][]any{vals}, uint64(len(vals))); err != nil {
			t.Fatalf("%s: encode %q: %v", c.typ, text, err)
		}
		want, _ := io.ReadAll(encodeWithClickHouseGo(t, c.typ, c.in...))
		if !bytes.HasSuffix(out.Bytes(), want) {
			t.Errorf("%s: re-encoded bytes differ for %q", c.typ, text)
		}
	}
}
