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
	"io"
	"testing"

	"github.com/ClickHouse/ch-go/compress"
)

// oneByteReader returns at most one byte per Read, forcing the embedded
// bufio.Reader to short-read on multi-byte fills. This simulates the TCP
// fragmentation case that exposes bare r.Read calls on the wire.
type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

// writeClientInfo writes a client-info block matching the field layout
// skipClientInfo reads at ServerRevision. hasSpan controls whether the
// OpenTelemetry 24-byte trace/span block is emitted.
func writeClientInfo(w *protoWriter, hasSpan bool) {
	w.putByte(1) // query kind = initial
	w.putStr("user")
	w.putStr("iqid")
	w.putStr("127.0.0.1")
	w.putInt64(1700000000) // initial_query_start_time — 8 bytes
	w.putByte(1)           // interface type = TCP
	w.putStr("os-user")
	w.putStr("host")
	w.putStr("client")
	w.putUvarint(1)              // version major
	w.putUvarint(0)              // version minor
	w.putUvarint(ServerRevision) // client protocol revision
	w.putStr("")                 // quota key
	w.putUvarint(0)              // distributed depth
	w.putUvarint(0)              // version patch
	if hasSpan {
		w.putByte(1)
		// trace id (16) + span id (8) = 24 bytes
		span := make([]byte, 24)
		for i := range span {
			span[i] = byte(i + 1)
		}
		w.Write(span)
		w.putStr("trace-state")
		w.putByte(0) // flags
	} else {
		w.putByte(0)
	}
	w.putUvarint(0) // parallel replicas
	w.putUvarint(0)
	w.putUvarint(0)
}

// TestSkipClientInfo_ShortReadStartTime verifies that skipClientInfo
// correctly consumes the 8-byte initial_query_start_time even when the
// underlying reader delivers one byte at a time. With a bare r.Read,
// the bufio.Reader short-reads and desynchronizes the stream.
func TestSkipClientInfo_ShortReadStartTime(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	writeClientInfo(w, false)
	w.putByte(0x5A) // sentinel immediately after client info

	r := newProtoReader(&oneByteReader{data: buf.Bytes()})
	if err := skipClientInfo(r, ServerRevision); err != nil {
		t.Fatalf("skipClientInfo: %v", err)
	}
	sentinel, err := r.ReadByte()
	if err != nil {
		t.Fatalf("sentinel read: %v", err)
	}
	if sentinel != 0x5A {
		t.Fatalf("stream desync: want sentinel %#x, got %#x", 0x5A, sentinel)
	}
}

// TestSkipClientInfo_ShortReadOpenTelemetry covers the 24-byte OTel
// trace/span block, which is also bare-Read.
func TestSkipClientInfo_ShortReadOpenTelemetry(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	writeClientInfo(w, true)
	w.putByte(0x5A)

	r := newProtoReader(&oneByteReader{data: buf.Bytes()})
	if err := skipClientInfo(r, ServerRevision); err != nil {
		t.Fatalf("skipClientInfo: %v", err)
	}
	sentinel, err := r.ReadByte()
	if err != nil {
		t.Fatalf("sentinel read: %v", err)
	}
	if sentinel != 0x5A {
		t.Fatalf("stream desync: want sentinel %#x, got %#x", 0x5A, sentinel)
	}
}

func TestReadSettingsAcrossRevisions(t *testing.T) {
	for _, test := range []struct {
		name     string
		revision uint64
		value    string
	}{
		{"legacy numeric", 54429, "7"},
		{"string setting", 54430, "value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer
			w := newProtoWriter(&data)
			w.putStr("setting")
			if test.revision <= 54429 {
				w.putUvarint(7)
			} else {
				w.putUvarint(0)
				w.putStr(test.value)
			}
			w.putStr("")
			settings, err := readSettings(newProtoReader(&data), test.revision)
			if err != nil {
				t.Fatal(err)
			}
			if settings["setting"] != test.value {
				t.Fatalf("setting = %q, want %q", settings["setting"], test.value)
			}
		})
	}
}

func TestSkipClientDataLimitationsAndCompression(t *testing.T) {
	var external bytes.Buffer
	newProtoWriter(&external).putStr("external")
	if err := skipClientData(newProtoReader(&external), ServerRevision, false); err == nil {
		t.Fatal("accepted external table block")
	}

	var inline bytes.Buffer
	w := newProtoWriter(&inline)
	w.putStr("")
	writeEmptyClientBlockContent(w, ServerRevision, 1, 1)
	if err := skipClientData(newProtoReader(&inline), ServerRevision, false); err == nil {
		t.Fatal("accepted inline data block")
	}

	var block bytes.Buffer
	writeEmptyClientBlockContent(newProtoWriter(&block), ServerRevision, 0, 0)
	cw := compress.NewWriter(compress.LevelZero, compress.LZ4)
	if err := cw.Compress(block.Bytes()); err != nil {
		t.Fatal(err)
	}
	var packet bytes.Buffer
	w = newProtoWriter(&packet)
	w.putStr("")
	packet.Write(cw.Data)
	if err := skipClientData(newProtoReader(&packet), ServerRevision, true); err != nil {
		t.Fatalf("compressed empty data block: %v", err)
	}

	var legacy bytes.Buffer
	w = newProtoWriter(&legacy)
	w.putStr("")
	w.putUvarint(0)
	w.putUvarint(0)
	if err := skipClientData(newProtoReader(&legacy), 0, false); err != nil {
		t.Fatalf("legacy empty data block: %v", err)
	}
}

func writeEmptyClientBlockContent(w *protoWriter, revision, columns, rows uint64) {
	if revision > 0 {
		w.putUvarint(0)
		w.putBool(false)
		w.putUvarint(2)
		w.putInt32(-1)
		w.putUvarint(0)
	}
	w.putUvarint(columns)
	w.putUvarint(rows)
}
