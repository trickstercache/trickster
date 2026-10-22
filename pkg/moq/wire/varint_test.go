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

package wire

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadVarintSpecExamples(t *testing.T) {
	// the example encodings given by the draft's variable-length integer section
	tests := []struct {
		in   string
		want uint64
	}{
		{"25", 37},
		{"8025", 37},
		{"bbbd", 15293},
		{"ed7f3e7d", 226442877},
		{"faa1a0e403d8", 2893212287960},
		{"fc8998abc66bc0", 151288809941952},
		{"fefa318fa8e3ca11", 70423237261249041},
		{"ffffffffffffffffff", math.MaxUint64},
	}
	for _, tc := range tests {
		b := mustHex(t, tc.in)
		v, n := ReadVarint(b)
		if v != tc.want || n != len(b) {
			t.Errorf("%s: got %d (%d bytes), want %d (%d bytes)", tc.in, v, n, tc.want, len(b))
		}
	}
}

func TestVarintRoundTripBoundaries(t *testing.T) {
	values := []uint64{
		0, 1, maxVarint1, maxVarint1 + 1, maxVarint2, maxVarint2 + 1, maxVarint3, maxVarint3 + 1,
		maxVarint4, maxVarint4 + 1, maxVarint5, maxVarint5 + 1, maxVarint6, maxVarint6 + 1, maxVarint7,
		maxVarint7 + 1, maxVarint8, maxVarint8 + 1, math.MaxUint64,
	}
	for _, v := range values {
		b := AppendVarint(nil, v)
		if len(b) != VarintLen(v) {
			t.Errorf("%d: encoded %d bytes, VarintLen says %d", v, len(b), VarintLen(v))
		}
		if VarintLenFromFirstByte(b[0]) != len(b) {
			t.Errorf("%d: first byte %#x implies %d bytes, encoded %d", v, b[0], VarintLenFromFirstByte(b[0]), len(b))
		}
		got, n := ReadVarint(b)
		if got != v || n != len(b) {
			t.Errorf("%d: decoded %d from %d bytes", v, got, n)
		}
		if _, n := ReadVarint(b[:len(b)-1]); len(b) > 1 && n != 0 {
			t.Errorf("%d: a truncated encoding decoded", v)
		}
	}
	if _, n := ReadVarint(nil); n != 0 {
		t.Error("empty input decoded")
	}
}

func TestAppendVarintNo7(t *testing.T) {
	v := uint64(maxVarint6 + 1)
	if VarintLen(v) != 7 {
		t.Fatalf("test value should need the 7-byte form")
	}
	b := AppendVarintNo7(nil, v)
	if len(b) != 8 {
		t.Fatalf("expected the 8-byte form, got %d bytes", len(b))
	}
	if got, _ := ReadVarint(b); got != v {
		t.Errorf("decoded %d, want %d", got, v)
	}
	if !bytes.Equal(AppendVarintNo7(nil, 5), AppendVarint(nil, 5)) {
		t.Error("values not needing 7 bytes should encode as usual")
	}
}

func FuzzReadVarint(f *testing.F) {
	for _, s := range []string{"25", "8025", "bbbd", "ffffffffffffffffff", "fe"} {
		f.Add(mustHex(f, s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, n := ReadVarint(b)
		if n == 0 {
			return
		}
		if n > len(b) || n > MaxVarintLen {
			t.Fatalf("consumed %d of %d bytes", n, len(b))
		}
		// any accepted value round-trips through the shortest encoding
		if got, m := ReadVarint(AppendVarint(nil, v)); got != v || m != VarintLen(v) {
			t.Fatalf("value %d did not round-trip", v)
		}
	})
}
