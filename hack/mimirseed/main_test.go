/*
 * Copyright 2026 The Trickster Authors
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

package main

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// decodeLiterals reverses Encode, reading literal elements only
func decodeLiterals(t *testing.T, b []byte) []byte {
	t.Helper()
	size, n := binary.Uvarint(b)
	b = b[n:]
	var out []byte
	for len(b) > 0 {
		tag := b[0]
		if tag&3 != 0 {
			t.Fatalf("tag %x is not a literal", tag)
		}
		length := int(tag>>2) + 1
		b = b[1:]
		if tag>>2 == 61 {
			length = int(binary.LittleEndian.Uint16(b)) + 1
			b = b[2:]
		}
		out = append(out, b[:length]...)
		b = b[length:]
	}
	if uint64(len(out)) != size {
		t.Fatalf("decoded %d bytes, header says %d", len(out), size)
	}
	return out
}

func TestEncode(t *testing.T) {
	for _, n := range []int{0, 1, 60, 61, 70000, 1 << 17} {
		data := bytes.Repeat([]byte{7}, n)
		if got := decodeLiterals(t, Encode(data)); !bytes.Equal(got, data) {
			t.Fatalf("round trip of %d bytes failed", n)
		}
	}
}

func TestWriteRequest(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	b := WriteRequest(Topology["east"], start, start.Add(interval))
	if len(b) == 0 || !bytes.Contains(b, []byte(Metric)) || !bytes.Contains(b, []byte(BackupLabel)) {
		t.Fatal("the request lacks the metric or the backup label")
	}
	if got := sortLabels([][2]string{{"pod", "a"}, {"__name__", "x"}, {"isBackup", "true"}}); got[0][0] != "__name__" ||
		got[1][0] != "isBackup" {
		t.Fatalf("labels not sorted: %v", got)
	}
}

func TestRegionURLs(t *testing.T) {
	urls, err := regionURLs("")
	if err != nil || len(urls) != 2 {
		t.Fatalf("default regions: %v %v", urls, err)
	}
	if _, err := regionURLs("north=http://x"); err == nil {
		t.Fatal("a region outside the topology is refused")
	}
}
