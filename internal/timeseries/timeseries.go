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

import "time"

// Timeseries ...
type Timeseries interface {
	// SetExtents sets the Extents of the Timeseries
	SetExtents([]Extent)
	// Extents should return the list of time Extents having data present in the Timeseries
	Extents() []Extent
	// Step should return the Step Interval of the Timeseries
	Step() time.Duration
	// SetStep should update the Step Interval of the Timeseries
	SetStep(time.Duration)
	// Merge should merge the Timeseries collection into the source Timeseries
	Merge(...Timeseries)
	// Sort should uniqueify and sort all series by Timestamp
	Sort()
	// Copy should returns an exact duplicate source the Timeseries
	Copy() Timeseries
	// Crop should return a cropped copy of the Timeseries, leaving the original unchanged
	Crop(Extent) Timeseries
}
