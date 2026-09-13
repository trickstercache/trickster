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

func TestWriteBlockContentNoRows(t *testing.T) {
	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	cols := []Column{{Name: "x", Type: "UInt32"}}
	if err := writeBlockContent(w, cols, nil, 0); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestWriteFormatBlockRevisionFraming(t *testing.T) {
	columns := []Column{{Name: "x", Type: "UInt8"}}
	values := [][]any{{uint8(7)}}
	for _, test := range []struct {
		name, want string
		revision   uint64
	}{
		{"legacy", "\x01\x01\x01x\x05UInt8\x07", 0},
		{"before custom serialization", "\x01\x00\x02\xff\xff\xff\xff\x00\x01\x01\x01x\x05UInt8\x07", RevisionCustomSerialization - 1},
		{"custom serialization", "\x01\x00\x02\xff\xff\xff\xff\x00\x01\x01\x01x\x05UInt8\x00\x07", RevisionCustomSerialization},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := EncodeNativeFormat(&out, columns, values, 1, test.revision); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != test.want {
				t.Fatalf("framing = %x, want %x", got, test.want)
			}
		})
	}
}

func TestWriteCompressedDataBlockRevision(t *testing.T) {
	columns := []Column{{Name: "x", Type: "UInt8"}}
	values := [][]any{{uint8(7)}}
	var compressed bytes.Buffer
	if err := writeCompressedDataBlockRevision(
		newProtoWriter(&compressed), columns, values, 1, RevisionCustomSerialization-1,
	); err != nil {
		t.Fatal(err)
	}
	packet := compressed.Bytes()
	if len(packet) < 3 || packet[0] != ServerData || packet[1] != 0 {
		t.Fatalf("invalid compressed packet envelope: %x", packet)
	}
	var want bytes.Buffer
	if err := EncodeNativeFormat(&want, columns, values, 1, RevisionCustomSerialization-1); err != nil {
		t.Fatal(err)
	}
	decoded := make([]byte, want.Len())
	if _, err := io.ReadFull(compress.NewReader(bytes.NewReader(packet[2:])), decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, want.Bytes()) {
		t.Fatalf("decoded block = %x, want %x", decoded, want.Bytes())
	}
}

func TestEncodeNativeBlock(t *testing.T) {
	var out bytes.Buffer
	if err := EncodeNativeBlock(&out, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("expected block framing")
	}
}
