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
	"math"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func TestZone(t *testing.T) {
	rng := weaktest.NewRand(9, 9)
	for _, name := range []string{"UTC", "America/New_York", "Asia/Kolkata", "Australia/Lord_Howe", "Europe/Dublin"} {
		loc, err := time.LoadLocation(name)
		require.NoError(t, err)
		z := NewZone(loc)
		require.Same(t, loc, z.Location())
		// runs of nearby epochs, which share periods, and far jumps, which don't
		e := int64(0)
		for i := range 20000 {
			if i%50 == 0 {
				e = rng.Int64N(math.MaxInt64) - math.MaxInt64/2
			}
			e += rng.Int64N(int64(6 * time.Hour))
			tm := time.Unix(0, e).In(loc)
			_, off := tm.Zone()
			require.Equal(t, int64(off)*int64(time.Second), z.Offset(Epoch(e)), "%s %s", name, tm)
			local := z.Local(Epoch(e))
			require.Equal(t, Epoch(e+int64(off)*int64(time.Second)), local)
			// a local reading maps back to an epoch reading the same, and to e itself unless repeated
			back := z.FromLocal(local)
			require.Equal(t, local, z.Local(back), "%s %s", name, tm)
			civil := time.Unix(0, int64(local)).UTC()
			want := time.Date(civil.Year(), civil.Month(), civil.Day(), civil.Hour(), civil.Minute(), civil.Second(),
				civil.Nanosecond(), loc)
			if want.UnixNano() == e {
				require.Equal(t, Epoch(e), back)
			}
		}
	}
	// a reading the clock skips resolves as time.Date does
	ny, _ := time.LoadLocation("America/New_York")
	skipped := Epoch(time.Date(2026, 3, 8, 2, 30, 0, 0, time.UTC).UnixNano())
	require.Equal(t, Epoch(time.Date(2026, 3, 8, 2, 30, 0, 0, ny).UnixNano()), NewZone(ny).FromLocal(skipped))
	// the epochs' extremes are in unbounded periods
	z := NewZone(ny)
	for _, e := range []int64{math.MaxInt64, math.MinInt64} {
		_, off := time.Unix(0, e).In(ny).Zone()
		require.Equal(t, int64(off)*int64(time.Second), z.Offset(Epoch(e)))
	}
}
