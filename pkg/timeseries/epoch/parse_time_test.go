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

package epoch

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

const naiveLayout = "2006-01-02T15:04:05.999999999"

var canonicalTimes = []string{
	"2026-09-30T12:34:56Z",
	"2026-09-30T12:34:56.5Z",
	"2026-09-30T12:34:56.123456789Z",
	"2026-09-30T12:34:56.1234567891234Z",
	"1970-01-01T00:00:00Z",
	"1969-12-31T23:59:59.999999999Z",
	"0000-01-01T00:00:00Z",
	"0000-02-29T00:00:00Z",
	"0001-03-01T00:00:00Z",
	"9999-12-31T23:59:59.999999999Z",
	"2024-02-29T00:00:00Z",
	"2000-02-29T00:00:00Z",
	"1600-02-29T00:00:00Z",
	"2262-04-11T23:47:16.854775807Z",
	"2262-04-11T23:47:16.854775808Z",
	"1677-09-21T00:12:43.145224192Z",
	"1677-09-21T00:12:43.145224191Z",
	"2026-09-30T12:34:56",
	"2026-09-30T12:34:56.000Z",
	// forms the fast path leaves to time.Parse, or that nothing accepts
	"2023-02-29T00:00:00Z",
	"1900-02-29T00:00:00Z",
	"2026-04-31T00:00:00Z",
	"2026-13-01T00:00:00Z",
	"2026-00-01T00:00:00Z",
	"2026-01-00T00:00:00Z",
	"2026-01-01T24:00:00Z",
	"2026-01-01T00:60:00Z",
	"2026-01-01T00:00:60Z",
	"2026-01-01T0:00:00Z",
	"2026-01-01 00:00:00Z",
	"2026-01-01T00:00:00.Z",
	"2026-01-01T00:00:00,5Z",
	"2026-01-01T00:00:00+00:00",
	"2026-01-01T00:00:00-07:00",
	"2026-01-01T00:00:00z",
	"2026-01-01T00:00:00ZZ",
	"+026-01-01T00:00:00Z",
	"-026-01-01T00:00:00Z",
	"2026-01-01",
	"",
}

// parses s as the fast path's fallback would, returning ok false where time.Parse fails
func parseWithTime(s string, zoned bool) (Epoch, bool) {
	var t time.Time
	var err error
	if zoned {
		t, err = time.Parse(time.RFC3339Nano, s)
	} else {
		t, err = time.ParseInLocation(naiveLayout, s, time.UTC)
	}
	if err != nil {
		return 0, false
	}
	return Epoch(t.UnixNano()), true
}

func checkCanonicalTime(t *testing.T, s string) {
	t.Helper()
	for _, zoned := range []bool{true, false} {
		got, ok := ParseCanonicalTime([]byte(s), zoned)
		if !ok {
			continue
		}
		want, wantOK := parseWithTime(s, zoned)
		if !wantOK || got != want {
			t.Fatalf("%q zoned=%t: got %d, time.Parse got %d (ok %t)", s, zoned, got, want, wantOK)
		}
		// the RFC 3339 layout without a fraction accepts one too, as time.Parse always does
		if zoned {
			alt, err := ParseRFC3339([]byte(s), time.RFC3339)
			if err != nil || alt != got {
				t.Fatalf("%q with RFC3339: got %d %v, want %d", s, alt, err, got)
			}
		}
	}
}

func TestParseCanonicalTime(t *testing.T) {
	for _, s := range canonicalTimes {
		checkCanonicalTime(t, s)
	}
	cases := []struct {
		in    string
		zoned bool
		want  bool
	}{
		{"2026-09-30T12:34:56Z", true, true},
		{"2026-09-30T12:34:56Z", false, false},
		{"2026-09-30T12:34:56.25", false, true},
		{"2026-09-30T12:34:56.25", true, false},
		{"2023-02-29T00:00:00Z", true, false},
		{"2026-01-01T00:00:00+00:00", true, false},
	}
	for _, c := range cases {
		if _, ok := ParseCanonicalTime([]byte(c.in), c.zoned); ok != c.want {
			t.Errorf("%q zoned=%t: got ok %t, want %t", c.in, c.zoned, ok, c.want)
		}
	}
}

func TestParseRFC3339(t *testing.T) {
	for _, s := range canonicalTimes {
		for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
			want, werr := time.Parse(layout, s)
			got, err := ParseRFC3339([]byte(s), layout)
			switch {
			case (err == nil) != (werr == nil):
				t.Errorf("%q %s: got error %v, time.Parse got %v", s, layout, err, werr)
			case err == nil && got != Epoch(want.UnixNano()):
				t.Errorf("%q %s: got %d, want %d", s, layout, got, want.UnixNano())
			}
		}
	}
	if n := testing.AllocsPerRun(100, func() {
		_, _ = ParseRFC3339([]byte("2026-09-30T12:34:56.123456789Z"), time.RFC3339Nano)
	}); n != 0 {
		t.Errorf("a canonical time allocated %v times", n)
	}
}

func TestParseCanonicalTimeRandom(t *testing.T) {
	rng := weaktest.NewRand(3, 9)
	for range 200_000 {
		year, month := rng.IntN(10000), 1+rng.IntN(12)
		day := 1 + rng.IntN(int(daysIn(int64(month), int64(year))))
		s := fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d", year, month, day, rng.IntN(24), rng.IntN(60),
			rng.IntN(60))
		if digits := rng.IntN(13); digits > 0 {
			s += "."
			for range digits {
				s += string(rune('0' + rng.IntN(10)))
			}
		}
		if _, ok := ParseCanonicalTime([]byte(s), false); !ok {
			t.Fatalf("%q: not parsed", s)
		}
		checkCanonicalTime(t, s)
		checkCanonicalTime(t, s+"Z")
	}
}

func FuzzParseCanonicalTime(f *testing.F) {
	for _, s := range canonicalTimes {
		f.Add(s)
	}
	f.Fuzz(checkCanonicalTime)
}

func checkAppendCanonicalTime(t *testing.T, e Epoch) {
	t.Helper()
	tm := time.Unix(0, int64(e)).UTC()
	for _, c := range []struct {
		layout          string
		fraction, zoned bool
	}{
		{time.RFC3339, false, true}, {time.RFC3339Nano, true, true}, {naiveLayout, true, false},
	} {
		want := tm.AppendFormat(nil, c.layout)
		if got := AppendCanonicalTime(nil, e, c.fraction, c.zoned); string(got) != string(want) {
			t.Fatalf("%d as %s: got %s, want %s", e, c.layout, got, want)
		}
	}
}

func TestAppendCanonicalTime(t *testing.T) {
	for _, e := range []Epoch{0, 1, -1, 999999999, -999999999, 1e9, -1e9, 1577836800123456789, 1577836800100000000,
		math.MaxInt64, math.MinInt64, 253402300799999999999 % math.MaxInt64, -62135596800000000000 % math.MaxInt64} {
		checkAppendCanonicalTime(t, e)
	}
	rng := weaktest.NewRand(10, 10)
	for range 200_000 {
		checkAppendCanonicalTime(t, Epoch(rng.Int64()))
		checkAppendCanonicalTime(t, Epoch(rng.Int64N(4e18)-2e18)/Epoch(1+rng.IntN(1e6))*Epoch(1+rng.IntN(1e6)))
	}
	if n := testing.AllocsPerRun(100, func() {
		_ = AppendCanonicalTime(make([]byte, 0, 64), 1577836800123456789, true, true)
	}); n > 1 {
		t.Errorf("%v allocations", n)
	}
}

func FuzzAppendCanonicalTime(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1577836800123456789))
	f.Fuzz(func(t *testing.T, e int64) { checkAppendCanonicalTime(t, Epoch(e)) })
}

func BenchmarkCanonicalTime(b *testing.B) {
	e := Epoch(1577836800123456789)
	raw := []byte("2020-01-01T00:00:00.123456789Z")
	buf := make([]byte, 0, 64)
	b.Run("append", func(b *testing.B) {
		for b.Loop() {
			buf = AppendCanonicalTime(buf[:0], e, true, true)
		}
	})
	b.Run("append/time", func(b *testing.B) {
		for b.Loop() {
			buf = time.Unix(0, int64(e)).UTC().AppendFormat(buf[:0], time.RFC3339Nano)
		}
	})
	b.Run("append/naive", func(b *testing.B) {
		for b.Loop() {
			buf = AppendCanonicalTime(buf[:0], e, true, false)
		}
	})
	b.Run("append/naive/time", func(b *testing.B) {
		for b.Loop() {
			buf = time.Unix(0, int64(e)).UTC().AppendFormat(buf[:0], naiveLayout)
		}
	})
	b.Run("parse", func(b *testing.B) {
		for b.Loop() {
			_, _ = ParseRFC3339(raw, time.RFC3339Nano)
		}
	})
	b.Run("parse/time", func(b *testing.B) {
		for b.Loop() {
			_, _ = time.Parse(time.RFC3339Nano, string(raw))
		}
	})
}
