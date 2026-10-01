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

package zstd

import (
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
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
	if !errors.Is(err, zstd.ErrMagicMismatch) {
		t.Errorf("expected ErrMagicMismatch, got %v", err)
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

	w = httptest.NewRecorder()
	enc = NewEncoder(w, 1)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}

	w = httptest.NewRecorder()
	enc = NewEncoder(w, 4)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}

	w = httptest.NewRecorder()
	enc = NewEncoder(w, 9)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}
}

func TestDecompress(t *testing.T) {
	t.Run("plain bytes unchanged", func(t *testing.T) {
		input := []byte(`{"status":"ok"}`)
		got := Decompress(input)
		if !bytes.Equal(got, input) {
			t.Errorf("expected unchanged, got %q", got)
		}
	})

	t.Run("zstd roundtrip", func(t *testing.T) {
		want := []byte(`{"status":"ok"}`)
		zb, err := Encode(want)
		if err != nil {
			t.Fatal(err)
		}
		got := Decompress(zb)
		if !bytes.Equal(got, want) {
			t.Errorf("expected %q, got %q", want, got)
		}
	})
}

func TestPooledCodecsRoundtrip(t *testing.T) {
	want := bytes.Repeat([]byte(`{"metric":{"job":"node"},"value":[1700000000,"1"]}`), 4096)
	for _, level := range []int{0, 1, 3, 5, 9} {
		for range 3 {
			var buf bytes.Buffer
			enc := NewEncoder(&buf, level)
			if _, err := enc.Write(want); err != nil {
				t.Fatal(err)
			}
			if err := enc.Close(); err != nil {
				t.Fatal(err)
			}
			// a second close must not return the encoder to its pool again
			if err := enc.Close(); err != nil {
				t.Fatal(err)
			}
			dec := NewDecoder(bytes.NewReader(buf.Bytes()))
			got, err := io.ReadAll(dec)
			if err != nil {
				t.Fatal(err)
			}
			dec.Close()
			if !bytes.Equal(got, want) {
				t.Fatalf("level %d: round trip changed the body", level)
			}
		}
	}
}

func TestPooledCodecsStartNoGoroutines(t *testing.T) {
	body := bytes.Repeat([]byte("trickster "), 1<<16)
	// warm the pools, so that any goroutine a codec keeps would already be running
	var buf bytes.Buffer
	enc := NewEncoder(&buf, -1)
	enc.Write(body)
	enc.Close()
	encoded := bytes.Clone(buf.Bytes())
	before := runtime.NumGoroutine()
	for range 8 {
		buf.Reset()
		enc = NewEncoder(&buf, -1)
		enc.Write(body)
		dec := NewDecoder(bytes.NewReader(encoded))
		// part way through a stream is where a concurrent decoder has its goroutine running
		io.ReadFull(dec, make([]byte, 1))
		if after := runtime.NumGoroutine(); after > before {
			t.Fatalf("goroutines grew from %d to %d while codecs were open", before, after)
		}
		enc.Close()
		dec.Close()
	}
}

func TestEncoderLevel(t *testing.T) {
	cases := []struct {
		level int
		want  zstd.EncoderLevel
	}{
		{-1, zstd.SpeedDefault},
		{0, zstd.SpeedDefault},
		{1, zstd.SpeedFastest},
		{2, zstd.SpeedFastest},
		{3, zstd.SpeedDefault},
		{4, zstd.SpeedBetterCompression},
		{7, zstd.SpeedBetterCompression},
		{8, zstd.SpeedBestCompression},
		{22, zstd.SpeedBestCompression},
	}
	for _, c := range cases {
		if got := encoderLevel(c.level); got != c.want {
			t.Errorf("level %d: got %v want %v", c.level, got, c.want)
		}
	}
}

func TestNewDecoderBadInput(t *testing.T) {
	dec := NewDecoder(bytes.NewReader([]byte("not zstd at all")))
	if _, err := io.ReadAll(dec); err == nil {
		t.Error("expected an error decoding a stream that isn't zstd")
	}
	dec.Close()
}
