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

package dataset

import (
	"errors"
	"fmt"
	"testing"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// encodes s at an offset of prefix bytes into its buffer, and returns the blob starting at base
func encodeAt(t testing.TB, s Segments, prefix int) []byte {
	t.Helper()
	buf := make([]byte, prefix, prefix+64)
	out, err := AppendSegments(buf, prefix, s)
	if err != nil {
		t.Fatal(err)
	}
	return out[prefix:]
}

func TestSegmentsCodecRoundTrip(t *testing.T) {
	rng := weaktest.NewRand(17, 18)
	for iter := range 1000 {
		cols := rng.IntN(4)
		pts := randPoints(rng, rng.IntN(30), cols, 40, true, randProfiles(rng, cols))
		s := segmentsOf(pts, cols, rng.IntN(len(pts)+1))
		// a view keeps its parent's data, which only the rows it holds are encoded from
		start, end := epoch.Epoch(rng.IntN(200)), epoch.Epoch(200+rng.IntN(300))
		s = s.View(start, end)
		want := pointsOf(s)
		blob := encodeAt(t, s, rng.IntN(16))
		context := fmt.Sprintf("iteration %d", iter)
		for shift := range 3 {
			// the blob at an aligned address and at unaligned ones, which decode by copying
			in := blob
			if shift > 0 {
				buf := make([]byte, len(blob)+shift)
				in = buf[shift:]
				copy(in, blob)
			}
			got, pos, err := ReadSegments(in, 0)
			if err != nil {
				t.Fatalf("%s shift %d: %v", context, shift, err)
			}
			if pos != len(in) {
				t.Fatalf("%s: read %d of %d bytes", context, pos, len(in))
			}
			requireSamePoints(t, want, pointsOf(got), context)
			aligned := AlignedBlob(in)
			if got2, _, err := ReadSegments(aligned, 0); err != nil {
				t.Fatal(err)
			} else {
				requireSamePoints(t, want, pointsOf(got2), context+" aligned")
			}
		}
	}
}

func TestSegmentsCodecSharesAlignedBlob(t *testing.T) {
	pts := Points{{Epoch: 1, Values: []any{1.5, "a"}}, {Epoch: 2, Values: []any{2.5, "bc"}}}
	blob := AlignedBlob(encodeAt(t, Segments{segmentOf(pts, 2)}, 3))
	got, _, err := ReadSegments(blob, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := got[0].Epochs()
	start, end := uintptr(unsafe.Pointer(&blob[0])), uintptr(unsafe.Pointer(&blob[len(blob)-1]))
	if p := uintptr(unsafe.Pointer(&e[0])); littleEndian && (p < start || p > end) {
		t.Error("the decoded epochs don't share the blob")
	}
	if got[0].Size() <= 0 {
		t.Error("no size")
	}
}

func TestSegmentsCodecEmptyAndNull(t *testing.T) {
	blob := encodeAt(t, nil, 0)
	got, _, err := ReadSegments(blob, 0)
	if err != nil || got != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	nulls := Points{{Epoch: 1, Values: []any{nil}}, {Epoch: 2, Values: []any{nil}}}
	got, _, err = ReadSegments(encodeAt(t, Segments{segmentOf(nulls, 1)}, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	requireSamePoints(t, nulls, pointsOf(got), "nulls")
}

func TestSegmentsCodecUnencodable(t *testing.T) {
	pts := Points{{Epoch: 1, Values: []any{make(chan int)}}}
	if _, err := AppendSegments(nil, 0, Segments{segmentOf(pts, 1)}); err == nil {
		t.Error("a channel value encoded")
	}
}

func TestSegmentsCodecRejectsCorruption(t *testing.T) {
	rng := weaktest.NewRand(19, 20)
	pts := randPoints(rng, 20, 3, 40, true, []int{profileMixed, profileBytes, profileAny})
	blob := encodeAt(t, Segments{segmentOf(pts, 3)}, 0)
	for n := range len(blob) {
		// every truncation fails, and never panics
		if _, _, err := ReadSegments(blob[:n], 0); !errors.Is(err, ErrInvalidSegments) {
			t.Fatalf("a blob cut to %d of %d bytes gave %v", n, len(blob), err)
		}
	}
	if _, _, err := ReadSegments(blob, len(blob)+1); !errors.Is(err, ErrInvalidSegments) {
		t.Errorf("a position past the blob gave %v", err)
	}
	bad := []Column{
		{kind: KindMixed + 1, vals: []uint64{0}},
		{kind: KindMixed, vals: []uint64{0}},
		{kind: KindString, vals: []uint64{5}, data: []byte("ab")},
		{kind: KindExt, vals: []uint64{1}, ext: []any{1}},
		{kind: KindMixed, vals: []uint64{0}, tags: []Kind{KindMixed}},
		{kind: KindInt64, vals: []uint64{0}, tags: []Kind{}},
	}
	for i, c := range bad {
		if c.valid() {
			t.Errorf("column %d reported valid", i)
		}
	}
}

func TestSegmentsCodecByteCorruption(t *testing.T) {
	rng := weaktest.NewRand(23, 24)
	pts := randPoints(rng, 6, 3, 40, true, []int{profileMixed, profileBytes, profileAny})
	blob := encodeAt(t, Segments{segmentOf(pts, 3)}, 0)
	for i := range blob {
		for _, b := range []byte{0, 0xff, blob[i] ^ 0x5a} {
			// every corruption fails or decodes to values that can all be read
			bad := append([]byte(nil), blob...)
			bad[i] = b
			readAllValues(t, bad)
		}
	}
}

func readAllValues(t *testing.T, blob []byte) {
	t.Helper()
	s, _, err := ReadSegments(AlignedBlob(blob), 0)
	if err != nil {
		return
	}
	for i := range s {
		for c := range s[i].NumCols() {
			col := s[i].Col(c)
			for j := range col.Len() {
				_ = col.Value(j)
			}
		}
	}
}

func FuzzReadSegments(f *testing.F) {
	rng := weaktest.NewRand(21, 22)
	for range 8 {
		pts := randPoints(rng, rng.IntN(10), 2, 20, true, []int{profileAny, profileMixed})
		f.Add(encodeAt(f, Segments{segmentOf(pts, 2)}, 0))
	}
	f.Fuzz(func(t *testing.T, blob []byte) {
		// whatever decodes can be read in full
		readAllValues(t, blob)
	})
}

func TestRefersToNothing(t *testing.T) {
	rng := weaktest.NewRand(12, 12)
	for range 100_000 {
		tags := make([]Kind, rng.IntN(40))
		for i := range tags {
			tags[i] = Kind(rng.IntN(int(KindString)))
			if rng.IntN(64) == 0 {
				tags[i] = Kind(rng.IntN(256))
			}
		}
		want := true
		for _, k := range tags {
			if k >= KindString {
				want = false
			}
		}
		if got := refersToNothing(tags); got != want {
			t.Fatalf("%v: got %t, want %t", tags, got, want)
		}
	}
}
