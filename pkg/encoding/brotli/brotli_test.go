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

package brotli

import (
	"bytes"
	"io"
	"net/http/httptest"
	"testing"
)

func TestDecodeEncode(t *testing.T) {
	const expected = "trickster"
	b, err := Encode([]byte(expected))
	if err != nil {
		t.Error(err)
	}
	b, err = Decode(b)
	if err != nil {
		t.Error(err)
	}
	if string(b) != expected {
		t.Errorf("expected %s got %s", expected, string(b))
	}
}

func TestNewDecoder(t *testing.T) {
	const expected = "trickster"
	b, err := Encode([]byte(expected))
	if err != nil {
		t.Error(err)
	}
	r := bytes.NewReader(b)
	dec := NewDecoder(r)
	if dec == nil {
		t.Error("expected non-nil decoder")
	}
}

func TestNewEncoder(t *testing.T) {
	w := httptest.NewRecorder()
	enc := NewEncoder(w, 0)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}
}

func TestPooledEncoderRoundtrip(t *testing.T) {
	for i := range 3 {
		var buf bytes.Buffer
		enc := NewEncoder(&buf, 4)
		data := []byte("trickster pooled encoder test")
		enc.Write(data)
		enc.Close() // returns encoder to pool

		decoded, err := Decode(buf.Bytes())
		if err != nil {
			t.Fatalf("iteration %d: decode error: %v", i, err)
		}
		if string(decoded) != string(data) {
			t.Fatalf("iteration %d: expected %q got %q", i, data, decoded)
		}
	}
}

func TestPooledCodecsRoundtripLevels(t *testing.T) {
	want := bytes.Repeat([]byte("trickster pooled codec "), 512)
	for _, level := range []int{-1, 0, 1, 4, 11, 12} {
		var buf bytes.Buffer
		enc := NewEncoder(&buf, level)
		enc.Write(want)
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}
		dec := NewDecoder(bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(dec)
		dec.Close()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("level %d: round trip failed: %v", level, err)
		}
	}
}
