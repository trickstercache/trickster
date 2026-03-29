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

package segments

// U: point type (time.Time, int64, etc.)
// S: segment type (Extent, Range, etc.)

// Segment represents a range with inclusive Start/End points of type U.
type Segment[U any] interface {
	StartVal() U
	EndVal() U
	NewSegment(start, end U) Segment[U]
	String() string
}

// Diffabble is a named type of a slice of an implementation of Segment that
// can be passed into Difference() as diff D
type Diffabble[P any, StepT any] interface {
	Add(a P, step StepT) P
	Less(a, b P) bool
	Equal(a, b P) bool
	Zero() P
	Neg(step StepT) StepT
}

// Diff compares haves to needs and returns a []Segments missing from needs.
// Both haves and needs must be sorted by start value.
// This operates on segment boundaries in O(N+M) time rather than iterating
// individual timestamps.
func Diff[P any, S Segment[P], StepT comparable, D Diffabble[P, StepT]](
	haves []S, needs []S, step StepT, diff D,
) []S {
	var zero StepT
	if len(haves) == 0 {
		return needs
	}
	if len(needs) == 0 || step == zero {
		return nil
	}
	out := make([]S, 0, len(needs)*2)
	j := 0 // pointer into haves

	for _, n := range needs {
		if diff.Less(n.EndVal(), n.StartVal()) {
			continue
		}
		// cursor tracks the start of the current uncovered region within n
		cursor := n.StartVal()

		// advance j past haves that end before this need starts
		for j < len(haves) && diff.Less(haves[j].EndVal(), cursor) {
			j++
		}

		// walk through haves that overlap with [cursor, n.End]
		for hi := j; hi < len(haves); hi++ {
			h := haves[hi]
			if diff.Less(n.EndVal(), h.StartVal()) {
				break
			}
			if diff.Less(cursor, h.StartVal()) {
				gapEnd := diff.Add(h.StartVal(), diff.Neg(step))
				if !diff.Less(gapEnd, cursor) {
					out = append(out, n.NewSegment(cursor, gapEnd).(S))
				}
			}
			next := diff.Add(h.EndVal(), step)
			if diff.Less(cursor, next) {
				cursor = next
			}
		}

		if !diff.Less(n.EndVal(), cursor) {
			out = append(out, n.NewSegment(cursor, n.EndVal()).(S))
		}
	}
	return out
}
