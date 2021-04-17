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

import "testing"

func TestStartsAtOrAfter(t *testing.T) {

	e := Extent{Start: t101, End: t200}
	if !e.StartsAtOrAfter(t100) {
		t.Error(("expected true"))
	}

	if e.StartsAtOrAfter(t200) {
		t.Error(("expected false"))
	}

}

func TestEndsAtOrBefore(t *testing.T) {

	e := Extent{Start: t101, End: t200}
	if e.EndsAtOrBefore(t100) {
		t.Error(("expected false"))
	}

	if !e.EndsAtOrBefore(t201) {
		t.Error(("expected true"))
	}

}

// // StartsAtOrAfter returns true if t is equal to or after the Extent's start time
// func (e *Extent) StartsAtOrAfter(t time.Time) bool {
// 	return t.Equal(e.Start) || e.Start.After(t)
// }

// // EndsAtOrBefore returns true if t is equal to or earlier than the Extent's end time
// func (e *Extent) EndsAtOrBefore(t time.Time) bool {
// 	return t.Equal(e.End) || e.End.Before(t)
// }
