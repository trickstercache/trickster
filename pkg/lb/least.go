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
package lb

import "sync/atomic"

// Least returns the member with the lowest score. Members that tie for it, which is every
// member of an idle pool, share the picks by weighted rotation over pos rather than the first
// of them taking every one. It neither locks nor allocates, and visits each member at most
// twice. score must be cheap and must not be negative; it may read live stats.
func Least(members []*Member, pos *atomic.Uint64, score func(*Member) float64) *Member {
	if len(members) == 0 {
		return nil
	}
	best := 0
	low := score(members[0])
	tiedWeight := uint64(members[0].weight) // #nosec G115 -- a member's weight is at least 1
	ties := 1
	for i := 1; i < len(members); i++ {
		s := score(members[i])
		switch {
		case s < low:
			best, low, ties = i, s, 1
			tiedWeight = uint64(members[i].weight) // #nosec G115 -- at least 1
		case s == low:
			ties++
			tiedWeight += uint64(members[i].weight) // #nosec G115 -- at least 1
		}
	}
	if ties == 1 {
		return members[best]
	}
	// scores are live, so the tied set may have moved since the first pass: the walk is
	// bounded by the slice and falls back to the first member found at the low score
	k := pos.Add(1) % tiedWeight
	for i := best; i < len(members); i++ {
		if score(members[i]) != low {
			continue
		}
		w := uint64(members[i].weight) // #nosec G115 -- at least 1
		if k < w {
			return members[i]
		}
		k -= w
	}
	return members[best]
}
