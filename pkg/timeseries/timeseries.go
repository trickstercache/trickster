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

// Package timeseries defines the interface for managing time seres objects
// and provides time range manipulation capabilities
package timeseries

import "time"

// Second is 1B, because 1B Nanoseconds == 1 Second
const Second = 1000000000

// FastForwardUserDisableFlag is a string that is checked to determine if Fast Forward
// should be selectively disabled for the provided query
const FastForwardUserDisableFlag = "trickster-fast-forward:off"

// BackfillToleranceFlag is a string that is checked to determine if Backfill Tolerance
// should be adjusted for the provided query
const BackfillToleranceFlag = "trickster-backfill-tolerance:"

type timeSeriesCtxVal int

const (
	// TimeRangeQueryCtx is a unique Context identifier for Time Range Query information
	TimeRangeQueryCtx timeSeriesCtxVal = iota
	// RequestOptionsCtx is a unique Context identifier for Request Options information
	RequestOptionsCtx
)

// Timeseries represents a Response Object from a Timeseries Database
type Timeseries interface {
	// SetExtents sets the Extents of the Timeseries
	SetExtents(ExtentList)
	// Extents should return the list of time Extents having data present in the Timeseries
	Extents() ExtentList
	// VolatileExtents should return the list of time Extents in the cached Timeseries
	// that should be re-fetched
	VolatileExtents() ExtentList
	// SetVolatileExtents sets the list of time Extents in the cached Timeseries
	// that should be re-fetched
	SetVolatileExtents(ExtentList)
	// TimeStampCount should return the number of unique timestamps across the timeseries
	TimestampCount() int64
	// Step should return the Step Interval of the Timeseries
	Step() time.Duration
	// Merge should merge the Timeseries collection into the source Timeseries
	Merge(bool, ...Timeseries)
	// Sort should uniqueify and sort all series by Timestamp
	Sort()
	// Clone should return an exact duplicate source the Timeseries
	Clone() Timeseries
	// CroppedClone should return an exact duplicate source the Timeseries, excluding any
	// timestamps from the source Timeseries that fall outside of the provided extent
	CroppedClone(Extent) Timeseries
	// CropToRange should reduce time range of the Timeseries to the provided Extent
	CropToRange(Extent)
	// CropToSize should reduce time range of the Timeseries to the provided element size using
	// a least-recently-used methodology, while limiting the upper extent to the provided time,
	// in order to support backfill tolerance
	CropToSize(int, time.Time, Extent)
	// SeriesCount returns the number of individual Series in the Timeseries object
	SeriesCount() int
	// ValueCount returns the count of all values across all Series in the Timeseries object
	ValueCount() int64
	// Size returns the approximate memory byte size of the timeseries object
	Size() int64
	// SetTimeRangeQuery sets the TimeRangeQuery associated with the Timeseries
	SetTimeRangeQuery(*TimeRangeQuery)
}
