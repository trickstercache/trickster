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

package sticky

import (
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
)

// passage is what every plane's session keeps: the path it is pinned to, the path it is sent down,
// and what its lookup found
type passage struct {
	// Pins is the path the session is pinned to; its Depth is 0 when it has none.
	Pins Path
	// Chosen is the path the session is sent down, one level per ALB that picks a member for it.
	Chosen   Path
	key      flowkey.Value
	found    found
	rejected bool
}

func (s *passage) result() result {
	switch {
	case s.rejected:
		return resultRejected
	case s.found == foundInvalid:
		return resultInvalid
	case s.found == foundExpired:
		return resultExpired
	case s.found != foundPins:
		return resultMiss
	case s.Chosen != s.Pins:
		return resultRepick
	}
	return resultHit
}

// Pin returns the member hash the session pins the pick at level to, if every level before it
// was sent down its pin: a session moved at one level starts afresh below it.
func (s *passage) Pin(level int) (uint64, bool) {
	if level >= int(s.Pins.Depth) || level > int(s.Chosen.Depth) {
		return 0, false
	}
	for i := range level {
		if s.Chosen.Hashes[i] != s.Pins.Hashes[i] {
			return 0, false
		}
	}
	return s.Pins.Hashes[level], true
}

// Record notes the member picked at level, which is the path's last level so far.
func (s *passage) Record(level int, hash uint64) {
	if level < 0 || level >= lb.MaxPickDepth {
		return
	}
	s.Chosen.Hashes[level], s.Chosen.Depth = hash, uint8(level+1)
}
