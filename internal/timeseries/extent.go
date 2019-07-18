/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package timeseries

import (
	"fmt"
	"time"
)

// Extent describes the start and end times for a given range of data
type Extent struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	LastUsed time.Time `json:"-"`
}

// Includes returns true if the Extent includes the provided Time
func (e *Extent) Includes(t time.Time) bool {
	return !t.Before(e.Start) && !t.After(e.End)
}

// StartsAt returns true if the t is equal to the Extent's start time
func (e *Extent) StartsAt(t time.Time) bool {
	return t.Equal(e.Start)
}

// EndsAt returns true if the t is equal to the Extent's end time
func (e *Extent) EndsAt(t time.Time) bool {
	return t.Equal(e.End)
}

// After returns true if the range of the Extent is completely after the provided time
func (e *Extent) After(t time.Time) bool {
	return t.Before(e.Start)
}

// After returns true if the range of the Extent is completely after the provided time
func (e Extent) String() string {
	return fmt.Sprintf("%d-%d", e.Start.Unix(), e.End.Unix())
}
