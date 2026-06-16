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
