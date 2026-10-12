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

package l4

import "time"

// Compose returns one admission that asks each non-nil part in order, the first refusal deciding.
// One part is returned as itself. Several parts implement Holder and read that part's own hold.
func Compose(admissions ...Admission) Admission {
	var c composition
	for _, a := range admissions {
		if a == nil {
			continue
		}
		c.parts = append(c.parts, a)
		if a.Datagrams() {
			c.datagrams = append(c.datagrams, a)
		}
	}
	switch len(c.parts) {
	case 0:
		return nil
	case 1:
		return c.parts[0]
	}
	return &c
}

type composition struct {
	chain
}

func (c *composition) Hold(f Flow) time.Duration {
	// A holder is not judged again here. Any other part is re-checked, and the first denial wins.
	for _, a := range c.parts {
		if h, ok := a.(Holder); ok {
			if d := h.Hold(f); d > 0 {
				return d
			}
			continue
		}
		if a.Peer(f) != Allow {
			return DefaultDeniedHold
		}
	}
	return 0
}
