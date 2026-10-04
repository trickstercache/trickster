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

package timeseries

import "time"

// FloorToGrid returns the latest timestamp at or before value on the grid of step-sized
// buckets offset by phase from the Unix epoch. A non-positive step returns value unchanged.
func FloorToGrid(value time.Time, step, phase time.Duration) time.Time {
	if step <= 0 {
		return value
	}
	stepNS := step.Nanoseconds()
	phaseNS := phase.Nanoseconds()
	shifted := value.UnixNano() - phaseNS
	quotient := shifted / stepNS
	if shifted < 0 && shifted%stepNS != 0 {
		quotient--
	}
	return time.Unix(0, quotient*stepNS+phaseNS).In(value.Location())
}

// CeilToGrid returns value when it is on the grid of step-sized buckets offset by phase from
// the Unix epoch, and otherwise the next grid timestamp. A non-positive step returns value.
func CeilToGrid(value time.Time, step, phase time.Duration) time.Time {
	floor := FloorToGrid(value, step, phase)
	if step <= 0 || floor.Equal(value) {
		return floor
	}
	return floor.Add(step)
}

// OnGrid reports whether value falls exactly on the grid of step-sized buckets offset by phase
// from the Unix epoch. No value is on the grid of a non-positive step.
func OnGrid(value time.Time, step, phase time.Duration) bool {
	if step <= 0 {
		return false
	}
	return (value.UnixNano()-phase.Nanoseconds())%step.Nanoseconds() == 0
}

// ClampToGrid narrows e to the grid timestamps it holds (step-sized, offset by phase from the
// Unix epoch), returning false when it holds none; a non-positive step returns e unchanged.
func (e Extent) ClampToGrid(step, phase time.Duration) (Extent, bool) {
	if step <= 0 {
		return e, true
	}
	out := Extent{
		Start:    CeilToGrid(e.Start, step, phase),
		End:      FloorToGrid(e.End, step, phase),
		LastUsed: e.LastUsed,
	}
	return out, !out.Start.After(out.End)
}

// ClampToGrid narrows each extent to its grid timestamps, drops those holding none, and counts
// the off-grid extents. An on-grid list is returned uncopied.
func (el ExtentList) ClampToGrid(step, phase time.Duration) (ExtentList, int) {
	if step <= 0 {
		return el, 0
	}
	var out ExtentList
	var offGrid int
	for i, e := range el {
		if OnGrid(e.Start, step, phase) && OnGrid(e.End, step, phase) {
			if out != nil {
				out = append(out, e)
			}
			continue
		}
		if out == nil {
			out = append(make(ExtentList, 0, len(el)), el[:i]...)
		}
		offGrid++
		if clamped, ok := e.ClampToGrid(step, phase); ok {
			out = append(out, clamped)
		}
	}
	if out == nil {
		return el, 0
	}
	return out, offGrid
}

// LastCompleteLabel returns the label of the newest bucket that had ended by now, and false
// when the query's sample model has no buckets.
func (trq *TimeRangeQuery) LastCompleteLabel(now time.Time) (time.Time, bool) {
	if trq.Step <= 0 {
		return time.Time{}, false
	}
	switch trq.SampleModel {
	case SampleModelBucket, SampleModelStored:
		return FloorToGrid(now, trq.Step, trq.Phase).Add(-trq.Step), true
	case SampleModelBucketStop:
		return FloorToGrid(now, trq.Step, trq.Phase), true
	}
	return time.Time{}, false
}
