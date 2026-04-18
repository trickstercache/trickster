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
	"testing"
)

func TestProtoWriterReadRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)

	if err := w.putByte(42); err != nil {
		t.Fatal(err)
	}
	if err := w.putUvarint(12345); err != nil {
		t.Fatal(err)
	}
	if err := w.putStr("hello world"); err != nil {
		t.Fatal(err)
	}
	if err := w.putInt32(-99); err != nil {
		t.Fatal(err)
	}
	if err := w.putBool(true); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)

	b, err := r.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if b != 42 {
		t.Fatalf("expected byte 42, got %d", b)
	}

	uv, err := r.uvarint()
	if err != nil {
		t.Fatal(err)
	}
	if uv != 12345 {
		t.Fatalf("expected uvarint 12345, got %d", uv)
	}

	s, err := r.str()
	if err != nil {
		t.Fatal(err)
	}
	if s != "hello world" {
		t.Fatalf("expected string %q, got %q", "hello world", s)
	}

	i, err := r.int32()
	if err != nil {
		t.Fatal(err)
	}
	if i != -99 {
		t.Fatalf("expected int32 -99, got %d", i)
	}

	bv, err := r.boolean()
	if err != nil {
		t.Fatal(err)
	}
	if !bv {
		t.Fatal("expected true, got false")
	}
}

func TestHandshakeRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)

	// Write a ClientHello
	w.putByte(ClientHello)
	w.putStr("test-client")
	w.putUvarint(1) // major
	w.putUvarint(2) // minor
	w.putUvarint(ServerRevision)
	w.putStr("default")
	w.putStr("user")
	w.putStr("pass")

	r := newProtoReader(&buf)
	pkt, _ := r.ReadByte()
	if pkt != ClientHello {
		t.Fatalf("expected ClientHello packet type 0, got %d", pkt)
	}

	hello, err := readClientHello(r)
	if err != nil {
		t.Fatal(err)
	}
	if hello.ClientName != "test-client" {
		t.Fatalf("expected client name %q, got %q", "test-client", hello.ClientName)
	}
	if hello.Database != "default" {
		t.Fatalf("expected database %q, got %q", "default", hello.Database)
	}
	if hello.Username != "user" {
		t.Fatalf("expected username %q, got %q", "user", hello.Username)
	}
	if hello.Password != "pass" {
		t.Fatalf("expected password %q, got %q", "pass", hello.Password)
	}

	// Write ServerHello and verify it starts with the correct packet type
	var serverBuf bytes.Buffer
	sw := newProtoWriter(&serverBuf)
	if err := writeServerHello(sw); err != nil {
		t.Fatal(err)
	}

	sr := newProtoReader(&serverBuf)
	pktType, _ := sr.ReadByte()
	if pktType != ServerHello {
		t.Fatalf("expected ServerHello packet type 0, got %d", pktType)
	}
	name, _ := sr.str()
	if name != "Trickster" {
		t.Fatalf("expected server name %q, got %q", "Trickster", name)
	}
}

func TestWriteException(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)

	if err := writeException(w, 60, "DB::SyntaxException", "bad query"); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	pkt, _ := r.ReadByte()
	if pkt != ServerException {
		t.Fatalf("expected ServerException packet type %d, got %d", ServerException, pkt)
	}

	code, _ := r.int32()
	if code != 60 {
		t.Fatalf("expected error code 60, got %d", code)
	}

	name, _ := r.str()
	if name != "DB::SyntaxException" {
		t.Fatalf("expected exception name %q, got %q", "DB::SyntaxException", name)
	}

	msg, _ := r.str()
	if msg != "bad query" {
		t.Fatalf("expected message %q, got %q", "bad query", msg)
	}
}

func TestWriteEmptyBlock(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)

	if err := writeEmptyBlock(w); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	pkt, _ := r.ReadByte()
	if pkt != ServerData {
		t.Fatalf("expected ServerData packet type %d, got %d", ServerData, pkt)
	}
}

func TestWriteDataBlock(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)

	columns := []Column{
		{Name: "id", Type: "UInt32"},
		{Name: "name", Type: "String"},
	}
	values := [][]any{
		{uint32(1), uint32(2)},
		{"alice", "bob"},
	}

	if err := writeDataBlock(w, columns, values, 2); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	pkt, _ := r.ReadByte()
	if pkt != ServerData {
		t.Fatalf("expected ServerData, got %d", pkt)
	}

	// block name (empty)
	blockName, _ := r.str()
	if blockName != "" {
		t.Fatalf("expected empty block name, got %q", blockName)
	}

	// skip block info: is_overflows(uvarint), bucket_num(bool), bucket_size(uvarint), reserved(int32), reserved(uvarint)
	r.uvarint()
	r.boolean()
	r.uvarint()
	r.int32()
	r.uvarint()

	numCols, _ := r.uvarint()
	numRows, _ := r.uvarint()
	if numCols != 2 {
		t.Fatalf("expected 2 columns, got %d", numCols)
	}
	if numRows != 2 {
		t.Fatalf("expected 2 rows, got %d", numRows)
	}

	// First column: id UInt32
	colName, _ := r.str()
	if colName != "id" {
		t.Fatalf("expected column name %q, got %q", "id", colName)
	}
	colType, _ := r.str()
	if colType != "UInt32" {
		t.Fatalf("expected column type %q, got %q", "UInt32", colType)
	}
	// custom serialization flag
	r.boolean()
	// Two UInt32 values
	var val [4]byte
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 1 {
		t.Fatal("expected first value 1")
	}
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 2 {
		t.Fatal("expected second value 2")
	}

	// Second column: name String
	colName, _ = r.str()
	if colName != "name" {
		t.Fatalf("expected column name %q, got %q", "name", colName)
	}
	colType, _ = r.str()
	if colType != "String" {
		t.Fatalf("expected column type %q, got %q", "String", colType)
	}
	r.boolean() // custom serialization flag
	s1, _ := r.str()
	if s1 != "alice" {
		t.Fatalf("expected %q, got %q", "alice", s1)
	}
	s2, _ := r.str()
	if s2 != "bob" {
		t.Fatalf("expected %q, got %q", "bob", s2)
	}
}

func TestWritePong(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writePong(w); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != ServerPong {
		t.Fatalf("expected ServerPong (%d), got %d", ServerPong, buf.Bytes()[0])
	}
}

func TestWriteEndOfStream(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeEndOfStream(w); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != ServerEndOfStream {
		t.Fatalf("expected ServerEndOfStream (%d), got %d", ServerEndOfStream, buf.Bytes()[0])
	}
}

func TestWriteColumnDataNullable(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := []any{"hello", nil, "world"}
	if err := writeColumnData(w, "Nullable(String)", values); err != nil {
		t.Fatal(err)
	}
	r := newProtoReader(&buf)
	// Read null bitmap: 0, 1, 0
	b0, _ := r.ReadByte()
	b1, _ := r.ReadByte()
	b2, _ := r.ReadByte()
	if b0 != 0 || b1 != 1 || b2 != 0 {
		t.Fatalf("expected null bitmap [0,1,0], got [%d,%d,%d]", b0, b1, b2)
	}
	// Read values: "hello", "" (zero for null), "world"
	s0, _ := r.str()
	s1, _ := r.str()
	s2, _ := r.str()
	if s0 != "hello" || s1 != "" || s2 != "world" {
		t.Fatalf("expected [hello,,world], got [%s,%s,%s]", s0, s1, s2)
	}
}

func TestWriteColumnDataArray(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := []any{
		[]any{float64(1), float64(2)},
		[]any{float64(3)},
	}
	if err := writeColumnData(w, "Array(UInt32)", values); err != nil {
		t.Fatal(err)
	}
	r := newProtoReader(&buf)
	// Offsets: 2, 3 (cumulative)
	var off [8]byte
	r.Read(off[:])
	if binary.LittleEndian.Uint64(off[:]) != 2 {
		t.Fatal("expected first offset 2")
	}
	r.Read(off[:])
	if binary.LittleEndian.Uint64(off[:]) != 3 {
		t.Fatal("expected second offset 3")
	}
	// Inner data: 3 UInt32 values
	var val [4]byte
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 1 {
		t.Fatal("expected 1")
	}
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 2 {
		t.Fatal("expected 2")
	}
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 3 {
		t.Fatal("expected 3")
	}
}

func TestWriteColumnDataFixedString(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := []any{"ab", "abcdef", "x"}
	if err := writeColumnData(w, "FixedString(4)", values); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if len(data) != 12 { // 3 * 4 bytes
		t.Fatalf("expected 12 bytes, got %d", len(data))
	}
	if string(data[0:4]) != "ab\x00\x00" {
		t.Fatalf("expected 'ab\\0\\0', got %q", data[0:4])
	}
	if string(data[4:8]) != "abcd" {
		t.Fatalf("expected 'abcd' (truncated), got %q", data[4:8])
	}
	if string(data[8:12]) != "x\x00\x00\x00" {
		t.Fatalf("expected 'x\\0\\0\\0', got %q", data[8:12])
	}
}

func TestWriteColumnDataTuple(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	values := []any{
		[]any{"hello", float64(1)},
		[]any{"world", float64(2)},
	}
	if err := writeColumnData(w, "Tuple(String, UInt32)", values); err != nil {
		t.Fatal(err)
	}
	r := newProtoReader(&buf)
	// First sub-column: two strings
	s0, _ := r.str()
	s1, _ := r.str()
	if s0 != "hello" || s1 != "world" {
		t.Fatalf("expected [hello,world], got [%s,%s]", s0, s1)
	}
	// Second sub-column: two UInt32
	var val [4]byte
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 1 {
		t.Fatal("expected 1")
	}
	r.Read(val[:])
	if binary.LittleEndian.Uint32(val[:]) != 2 {
		t.Fatal("expected 2")
	}
}

func TestWriteCompressedDataBlock(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	columns := []Column{{Name: "x", Type: "UInt32"}}
	values := [][]any{{uint32(42)}}
	if err := writeCompressedDataBlock(w, columns, values, 1); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty compressed output")
	}
	// First byte should be ServerData
	if buf.Bytes()[0] != ServerData {
		t.Fatalf("expected ServerData, got %d", buf.Bytes()[0])
	}
}
