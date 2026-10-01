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
	"time"
)

// Zone converts epochs to and from a time zone's local clock. It keeps the offset of the zone period it
// last looked up, which nearby epochs share, so it isn't safe for concurrent use.
type Zone struct {
	loc *time.Location
	// the period [start, end) the offset, in nanoseconds east of UTC, holds for
	start, end Epoch
	off        int64
}

// NewZone returns a Zone for loc.
func NewZone(loc *time.Location) *Zone {
	return &Zone{loc: loc, start: 1}
}

// Location returns the zone's location.
func (z *Zone) Location() *time.Location {
	return z.loc
}

// Offset returns the zone's offset from UTC at e, in nanoseconds.
func (z *Zone) Offset(e Epoch) int64 {
	if e >= z.start && e < z.end {
		return z.off
	}
	t := time.Unix(0, int64(e)).In(z.loc)
	_, off := t.Zone()
	start, end := t.ZoneBounds()
	z.off, z.start, z.end = int64(off)*int64(time.Second), boundEpoch(start, math.MinInt64), boundEpoch(end, math.MaxInt64)
	return z.off
}

// boundEpoch returns a zone period's bound, or edge for an unbounded one or one past the epochs' range
func boundEpoch(t time.Time, edge int64) Epoch {
	const limit = math.MaxInt64 / int64(time.Second)
	if t.IsZero() || t.Unix() <= -limit || t.Unix() >= limit {
		return Epoch(edge)
	}
	return Epoch(t.UnixNano())
}

// Local returns e as the zone's clock reads it, as an epoch of that clock in UTC.
func (z *Zone) Local(e Epoch) Epoch {
	return e + Epoch(z.Offset(e))
}

// FromLocal returns the epoch at which the zone's clock reads local, an epoch of that clock in UTC. A
// clock reading the zone skips or repeats resolves as time.Date resolves it.
func (z *Zone) FromLocal(local Epoch) Epoch {
	off := z.Offset(local - Epoch(z.off))
	if e := local - Epoch(off); z.Offset(e) == off {
		return e
	}
	t := time.Unix(0, int64(local)).UTC()
	return Epoch(time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), z.loc).UnixNano())
}
