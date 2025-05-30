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

func Diff[P any, S Segment[P], StepT comparable, D Diffabble[P, StepT]](
	have []S,
	need []S,
	step StepT,
	diff D,
) []S {
	var zero StepT
	if len(need) == 0 || step == zero {
		return nil
	}
	out := make([]S, (len(have)+1)*len(need))
	var k int

	for _, n := range need {
		if !diff.Less(n.StartVal(), n.EndVal()) {
			continue
		}
		missStart := diff.Zero()
		haveIdx := 0
		inMiss := false

		for ts := n.StartVal(); !diff.Less(n.EndVal(), ts); ts = diff.Add(ts, step) {
			for haveIdx < len(have) && diff.Less(have[haveIdx].EndVal(), ts) {
				haveIdx++
			}
			inRange := false
			if haveIdx < len(have) {
				s := have[haveIdx].StartVal()
				e := have[haveIdx].EndVal()
				inRange = !diff.Less(ts, s) && !diff.Less(e, ts)
			}
			if !inRange {
				if !inMiss {
					missStart = ts
					inMiss = true
				}
			} else if inMiss {
				seg := n.NewSegment(missStart, diff.Add(ts, diff.Neg(step)))
				out[k] = seg.(S)
				k++
				inMiss = false
			}
		}
		if inMiss {
			seg := n.NewSegment(missStart, n.EndVal())
			out[k] = seg.(S)
			k++
		}
	}
	return out[:k]
}
