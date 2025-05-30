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

//go:generate go tool msgp

package timeseries

import (
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/segments"
)

// Extent describes the start and end times for a given range of data
type Extent struct {
	Start    time.Time `msg:"start" json:"start"`
	End      time.Time `msg:"end" json:"end"`
	LastUsed time.Time `msg:"lu" json:"-"`
}

// Implements segments.Segment[time.Time]
func (e Extent) StartVal() time.Time { return e.Start }
func (e Extent) EndVal() time.Time   { return e.End }
func (e Extent) NewSegment(start, end time.Time) segments.Segment[time.Time] {
	return Extent{Start: start, End: end}
}

// After returns true if the range of the Extent is completely after the provided time
func (e *Extent) After(t time.Time) bool {
	return t.Before(e.Start)
}

// String returns the string representation of the Extent
func (e Extent) String() string {
	return fmt.Sprintf("%d-%d", e.Start.UnixNano()/1000000, e.End.UnixNano()/1000000)
}
