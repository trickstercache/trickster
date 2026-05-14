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
	"math"
	"math/big"
	"testing"
	"time"
)

func TestToNumeric(t *testing.T) {
	if got := toNumeric[int32](float64(7.9)); got != 7 {
		t.Fatalf("float64 -> int32: got %d", got)
	}
	if got := toNumeric[uint32](float32(3.5)); got != 3 {
		t.Fatalf("float32 -> uint32: got %d", got)
	}
	if got := toNumeric[int64](int64(-42)); got != -42 {
		t.Fatalf("int64 passthrough: got %d", got)
	}
	if got := toNumeric[uint16](int32(99)); got != 99 {
		t.Fatalf("int32 -> uint16: got %d", got)
	}
	if got := toNumeric[int32](int(123)); got != 123 {
		t.Fatalf("int -> int32: got %d", got)
	}
	if got := toNumeric[uint64](uint64(1 << 40)); got != 1<<40 {
		t.Fatalf("uint64 passthrough: got %d", got)
	}
	if got := toNumeric[uint32](uint32(5)); got != 5 {
		t.Fatalf("uint32 passthrough: got %d", got)
	}
	if got := toNumeric[uint16](uint16(6)); got != 6 {
		t.Fatalf("uint16 passthrough: got %d", got)
	}
	if got := toNumeric[uint8](uint8(7)); got != 7 {
		t.Fatalf("uint8 passthrough: got %d", got)
	}
	if got := toNumeric[float64]("not a number"); got != 0 {
		t.Fatalf("unsupported -> zero: got %v", got)
	}
}

func TestToString(t *testing.T) {
	if s := toString("hello"); s != "hello" {
		t.Fatalf("string passthrough: got %q", s)
	}
	if s := toString([]byte("bytes")); s != "bytes" {
		t.Fatalf("[]byte: got %q", s)
	}
	if s := toString(42); s != "42" {
		t.Fatalf("int via fmt.Sprint: got %q", s)
	}
	if s := toString(nil); s != "<nil>" {
		t.Fatalf("nil via fmt.Sprint: got %q", s)
	}
}

func TestToDateTime(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()
	if got := toDateTime(ts); got != 1_700_000_000 {
		t.Fatalf("time.Time: got %d", got)
	}
	if got := toDateTime(uint32(42)); got != 42 {
		t.Fatalf("uint32 passthrough: got %d", got)
	}
	if got := toDateTime(float64(12345.0)); got != 12345 {
		t.Fatalf("float64: got %d", got)
	}
	if got := toDateTime(int64(99)); got != 99 {
		t.Fatalf("int64 fallback: got %d", got)
	}
}

func TestToDate(t *testing.T) {
	ts := time.Date(1970, 1, 11, 0, 0, 0, 0, time.UTC)
	if got := toDate(ts); got != 10 {
		t.Fatalf("time.Time: expected 10 days, got %d", got)
	}
	if got := toDate(uint16(7)); got != 7 {
		t.Fatalf("uint16 passthrough: got %d", got)
	}
	if got := toDate(int64(3)); got != 3 {
		t.Fatalf("int64 fallback: got %d", got)
	}
}

func TestToDate32(t *testing.T) {
	ts := time.Date(1970, 1, 6, 0, 0, 0, 0, time.UTC)
	if got := toDate32(ts); got != 5 {
		t.Fatalf("time.Time: expected 5 days, got %d", got)
	}
	if got := toDate32(int32(-100)); got != -100 {
		t.Fatalf("int32 passthrough: got %d", got)
	}
	if got := toDate32(int64(42)); got != 42 {
		t.Fatalf("int64 fallback: got %d", got)
	}
}

func TestToDateTime64(t *testing.T) {
	ts := time.Unix(1_700_000_000, 500_000_000).UTC()
	want := ts.UnixMilli()
	if got := toDateTime64(ts); got != want {
		t.Fatalf("time.Time: want %d, got %d", want, got)
	}
	if got := toDateTime64(int64(12345)); got != 12345 {
		t.Fatalf("int64 fallback: got %d", got)
	}
}

func TestToBigInt(t *testing.T) {
	bi := big.NewInt(999)
	if got := toBigInt(bi); got != bi {
		t.Fatal("*big.Int passthrough failed")
	}
	if got := toBigInt(int64(-7)); got.Int64() != -7 {
		t.Fatalf("int64: got %s", got.String())
	}
	if got := toBigInt(uint64(1 << 60)); got.Uint64() != 1<<60 {
		t.Fatalf("uint64: got %s", got.String())
	}
	if got := toBigInt(float64(1234)); got.Int64() != 1234 {
		t.Fatalf("float64: got %s", got.String())
	}
	// non-numeric float falls back to 0 via the SetString("NaN") guard
	if got := toBigInt(math.NaN()); got.Sign() != 0 {
		t.Fatalf("NaN float: got %s", got.String())
	}
	if got := toBigInt("12345678901234567890"); got.String() != "12345678901234567890" {
		t.Fatalf("string: got %s", got.String())
	}
	if got := toBigInt("not a number"); got.Sign() != 0 {
		t.Fatalf("bad string should be 0: got %s", got.String())
	}
	if got := toBigInt(struct{}{}); got.Sign() != 0 {
		t.Fatalf("unknown type should be 0: got %s", got.String())
	}
}

func TestWriteBigIntPositive(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeBigInt(w, int64(0x0102030405060708), 16); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if len(got) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(got))
	}
	// little-endian: low byte first
	want := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
	if !bytes.Equal(got[:8], want) {
		t.Fatalf("low 8 bytes: want %x, got %x", want, got[:8])
	}
	for i := 8; i < 16; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d should be 0, got %x", i, got[i])
		}
	}
}

func TestWriteBigIntNegative(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeBigInt(w, int64(-1), 16); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if len(got) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(got))
	}
	for i, b := range got {
		if b != 0xFF {
			t.Fatalf("byte %d: two's complement -1 should be 0xFF, got %x", i, b)
		}
	}
}

func TestWriteBigInt256(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	bi, _ := new(big.Int).SetString("340282366920938463463374607431768211456", 10) // 2^128
	if err := writeBigInt(w, bi, 32); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 32 {
		t.Fatalf("expected 32 bytes, got %d", buf.Len())
	}
	// 2^128 has bit 128 set — byte index 16 should be 0x01, others 0.
	got := buf.Bytes()
	if got[16] != 0x01 {
		t.Fatalf("byte 16 should be 0x01, got %x", got[16])
	}
}

func TestWriteUUID(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeUUID(w, "00112233-4455-6677-8899-aabbccddeeff"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("expected 16 bytes, got %d", buf.Len())
	}
	// first half: BE 0x0011223344556677 stored as LE
	got := buf.Bytes()
	if binary.LittleEndian.Uint64(got[0:8]) != 0x0011223344556677 {
		t.Fatalf("first half wrong: %x", got[0:8])
	}
	if binary.LittleEndian.Uint64(got[8:16]) != 0x8899aabbccddeeff {
		t.Fatalf("second half wrong: %x", got[8:16])
	}
}

func TestWriteUUIDInvalid(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeUUID(w, "not-a-uuid"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("expected 16 zero bytes, got %d", buf.Len())
	}
	for i, b := range buf.Bytes() {
		if b != 0 {
			t.Fatalf("byte %d expected 0, got %x", i, b)
		}
	}
}

func TestWriteIPv4(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeIPv4(w, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	// little-endian order: [4,3,2,1]
	want := []byte{4, 3, 2, 1}
	if !bytes.Equal(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestWriteIPv4Invalid(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeIPv4(w, "not-an-ip"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 4 {
		t.Fatalf("expected 4 bytes, got %d", buf.Len())
	}
}

func TestWriteIPv6(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeIPv6(w, "::1"); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if len(got) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(got))
	}
	if got[15] != 1 {
		t.Fatalf("last byte of ::1 should be 1, got %x", got[15])
	}
	for i := 0; i < 15; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d of ::1 should be 0, got %x", i, got[i])
		}
	}
}

func TestWriteIPv6Invalid(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeIPv6(w, "garbage"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("expected 16 bytes, got %d", buf.Len())
	}
}

func TestWriteValueTypes(t *testing.T) {
	cases := []struct {
		colType string
		v       any
		want    []byte
	}{
		{"UInt8", uint8(0xAB), []byte{0xAB}},
		{"Bool", uint8(1), []byte{0x01}},
		{"UInt16", uint16(0x1234), []byte{0x34, 0x12}},
		{"UInt32", uint32(0x01020304), []byte{0x04, 0x03, 0x02, 0x01}},
		{"UInt64", uint64(0x0102030405060708), []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}},
		{"Int8", int64(-2), []byte{0xFE}},
		{"Int16", int64(-1), []byte{0xFF, 0xFF}},
		{"Int32", int64(-1), []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{"Int64", int64(-1), []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"Enum8", int64(-2), []byte{0xFE}},
		{"Enum8('a'=1)", int64(1), []byte{0x01}},
		{"Enum16('a'=1)", int64(1), []byte{0x01, 0x00}},
		{"Decimal32(2)", int64(12345), []byte{0x39, 0x30, 0x00, 0x00}},
		{"Decimal64(2)", int64(1), []byte{0x01, 0, 0, 0, 0, 0, 0, 0}},
		{"Decimal(9,2)", int64(2), []byte{0x02, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, c := range cases {
		t.Run(c.colType, func(t *testing.T) {
			var buf bytes.Buffer
			w := newProtoWriter(&buf)
			if err := writeValue(w, c.colType, c.v); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), c.want) {
				t.Fatalf("want %x, got %x", c.want, buf.Bytes())
			}
		})
	}
}

func TestWriteValueFloat(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeValue(w, "Float32", float64(1.5)); err != nil {
		t.Fatal(err)
	}
	got := math.Float32frombits(binary.LittleEndian.Uint32(buf.Bytes()))
	if got != 1.5 {
		t.Fatalf("want 1.5, got %v", got)
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "Float64", float64(2.5)); err != nil {
		t.Fatal(err)
	}
	gotD := math.Float64frombits(binary.LittleEndian.Uint64(buf.Bytes()))
	if gotD != 2.5 {
		t.Fatalf("want 2.5, got %v", gotD)
	}
}

func TestWriteValueTemporal(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeValue(w, "DateTime", uint32(1_700_000_000)); err != nil {
		t.Fatal(err)
	}
	if v := binary.LittleEndian.Uint32(buf.Bytes()); v != 1_700_000_000 {
		t.Fatalf("DateTime: got %d", v)
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "DateTime64(3)", int64(123456789)); err != nil {
		t.Fatal(err)
	}
	if v := int64(binary.LittleEndian.Uint64(buf.Bytes())); v != 123456789 {
		t.Fatalf("DateTime64: got %d", v)
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "Date", uint16(100)); err != nil {
		t.Fatal(err)
	}
	if v := binary.LittleEndian.Uint16(buf.Bytes()); v != 100 {
		t.Fatalf("Date: got %d", v)
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "Date32", int32(-365)); err != nil {
		t.Fatal(err)
	}
	if v := int32(binary.LittleEndian.Uint32(buf.Bytes())); v != -365 {
		t.Fatalf("Date32: got %d", v)
	}
}

func TestWriteValueBigAndAddress(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeValue(w, "Int128", int64(1)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("Int128 should be 16 bytes, got %d", buf.Len())
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "UInt256", uint64(1)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 32 {
		t.Fatalf("UInt256 should be 32 bytes, got %d", buf.Len())
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "Decimal128(10)", int64(1)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("Decimal128 should be 16 bytes, got %d", buf.Len())
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "Decimal256(20)", int64(1)); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 32 {
		t.Fatalf("Decimal256 should be 32 bytes, got %d", buf.Len())
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "UUID", "00112233-4455-6677-8899-aabbccddeeff"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("UUID should be 16 bytes, got %d", buf.Len())
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "IPv4", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 4 {
		t.Fatalf("IPv4 should be 4 bytes, got %d", buf.Len())
	}

	buf.Reset()
	w = newProtoWriter(&buf)
	if err := writeValue(w, "IPv6", "::1"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 16 {
		t.Fatalf("IPv6 should be 16 bytes, got %d", buf.Len())
	}
}

func TestWriteValueDefaultString(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeValue(w, "String", "hi"); err != nil {
		t.Fatal(err)
	}
	r := newProtoReader(&buf)
	s, _ := r.str()
	if s != "hi" {
		t.Fatalf("want %q, got %q", "hi", s)
	}
}
