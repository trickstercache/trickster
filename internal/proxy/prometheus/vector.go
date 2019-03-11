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

package prometheus

import (
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
)

// Extents is present to conform to the timeseries.Timeseries interface, but is not used by the VectorEnvelope
func (ve VectorEnvelope) Extents() []timeseries.Extent {
	return nil
}

// CalculateDeltas is present to conform to the timeseries.Timeseries interface, but is not used by the VectorEnvelope
func (ve VectorEnvelope) CalculateDeltas(*timeseries.TimeRangeQuery) []timeseries.Extent {
	return nil
}

// SetStep is present to conform to the timeseries.Timeseries interface, but is not used by the VectorEnvelope
func (ve VectorEnvelope) SetStep(time.Duration) {}

// Merge is present to conform to the timeseries.Timeseries interface, but is not used by the VectorEnvelope
func (ve VectorEnvelope) Merge(ts ...timeseries.Timeseries) {}

// Sort ...
func (ve VectorEnvelope) Sort() {}

// SetExtents ...
func (ve VectorEnvelope) SetExtents([]timeseries.Extent) {}

// Crop ...
func (ve VectorEnvelope) Crop(e timeseries.Extent) timeseries.Timeseries {
	return ve
}

// Copy ...
func (ve VectorEnvelope) Copy() timeseries.Timeseries {
	// TO DO: Implement Copy
	return ve
}

// Step is present to conform to the timeseries.Timeseries interface, but is not used by the VectorEnvelope
func (ve VectorEnvelope) Step() time.Duration {
	return time.Duration(0 * time.Second)
}
