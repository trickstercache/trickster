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

package influxdb

import (
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
)

// SetExtents ...
func (se SeriesEnvelope) SetExtents([]timeseries.Extent) {}

// Extents ...
func (se SeriesEnvelope) Extents() []timeseries.Extent {
	return nil
}

// CalculateDeltas ...
func (se SeriesEnvelope) CalculateDeltas(trq *timeseries.TimeRangeQuery) []timeseries.Extent {
	return nil
}

// Step ...
func (se SeriesEnvelope) Step() time.Duration {
	return time.Duration(0 * time.Second)
}

// SetStep ...
func (se SeriesEnvelope) SetStep(time.Duration) {}

// Merge ...
func (se SeriesEnvelope) Merge(sort bool, ts ...timeseries.Timeseries) {}

// Copy ...
func (se SeriesEnvelope) Copy() timeseries.Timeseries {

	// TO DO: Implement Copy
	return se
}

// Crop ...
func (se SeriesEnvelope) Crop(e timeseries.Extent) timeseries.Timeseries {
	return se
}

// Sort ...
func (se SeriesEnvelope) Sort() {}

// SeriesCount returns the number of individual Series in the Timeseries object
func (se SeriesEnvelope) SeriesCount() int {
	return 0
}

// ValueCount returns the count of all values across all Series in the Timeseries object
func (se SeriesEnvelope) ValueCount() int {
	return 0
}
