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
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
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
	types := []string{TypeUInt16, TypeUInt32, TypeUInt64, TypeInt16,
		TypeInt32, TypeInt64, TypeFloat32, TypeFloat64, TypeDateTime, TypeDate,
		"DateTime64(3)"}
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
	for i := 0; i < 40; i++ {
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
		for i := 0; i < rows; i++ {
			_ = writeNativeValue(b, "UInt64",
				strconv.FormatInt(1577836800000+int64(i)*60000, 10))
		}
		_ = writeNativeString(b, "hostname")
		_ = writeNativeString(b, "String")
		_, _ = b.Write([]byte{0})
		for i := 0; i < rows; i++ {
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

