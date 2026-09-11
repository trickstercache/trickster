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

package gzip

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/gzip"
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

	_, err = Decode([]byte(expected))
	if !errors.Is(err, gzip.ErrHeader) {
		t.Errorf("expected gzip.ErrHeader, got %v", err)
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

	_, err = Decode([]byte(expected))
	if !errors.Is(err, gzip.ErrHeader) {
		t.Errorf("expected gzip.ErrHeader, got %v", err)
	}
}

func TestNewEncoder(t *testing.T) {
	w := httptest.NewRecorder()
	enc := NewEncoder(w, -1)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}
}

func TestPooledEncoderRoundtrip(t *testing.T) {
	for i := range 3 {
		var buf bytes.Buffer
		enc := NewEncoder(&buf, -1)
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

func TestDecompress(t *testing.T) {
	t.Run("plain bytes returned unchanged", func(t *testing.T) {
		input := []byte(`{"status":"ok"}`)
		got := Decompress(input)
		if !bytes.Equal(got, input) {
			t.Errorf("expected input unchanged, got %q", got)
		}
	})

	t.Run("gzip-compressed bytes returned decompressed", func(t *testing.T) {
		want := []byte(`{"status":"ok"}`)
		compressed, err := Encode(want)
		if err != nil {
			t.Fatal(err)
		}
		got := Decompress(compressed)
		if !bytes.Equal(got, want) {
			t.Errorf("expected %q, got %q", want, got)
		}
	})

	t.Run("empty input returned unchanged", func(t *testing.T) {
		got := Decompress([]byte{})
		if len(got) != 0 {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("truncated gzip returned unchanged", func(t *testing.T) {
		input := []byte{0x1f, 0x8b}
		got := Decompress(input)
		if !bytes.Equal(got, input) {
			t.Errorf("expected input unchanged, got %q", got)
		}
	})
}
