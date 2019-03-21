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

// TimeRangeQuery ...
type TimeRangeQuery struct {
	// Statement is the timeseries database query (with tokenized timeranges where present) requested by the user
	Statement string
	// Extent provides the start and end times for the request from a timeseries database
	Extent Extent
	// Step indicates the amount of time in seconds between each datapoint in a TimeRangeQuery's resulting timeseries
	Step int64
}

// NormalizeExtent adjusts the Start and End of a TimeRangeQuery's Extent to align against normalized boundaries.
func (trq *TimeRangeQuery) NormalizeExtent() {
	if trq.Step > 0 {
		if trq.Extent.End.After(time.Now()) {
			fmt.Println("Extent Start is", trq.Extent.Start, "End WAS", trq.Extent.End, "and is NOW", time.Now())
			trq.Extent.End = time.Now()
		}

		fmt.Println("Extent Start was", trq.Extent.Start, "End Was", trq.Extent.End, "Step is", trq.Step)
		trq.Extent.Start = time.Unix((trq.Extent.Start.Unix()/trq.Step)*trq.Step, 0)
		trq.Extent.End = time.Unix((trq.Extent.End.Unix()/trq.Step)*trq.Step, 0)
		fmt.Println("Extent Start normalized to", trq.Extent.Start, "End normalized to", trq.Extent.End)

	}
}

// CalculateDeltas provides a list of extents that are not in a cached timeseries, when provided a list of extents that are cached.
func (trq *TimeRangeQuery) CalculateDeltas(have []Extent) []Extent {
	misses := []time.Time{}
	for i := trq.Extent.Start; trq.Extent.End.After(i) || trq.Extent.End == i; i = i.Add(time.Second * time.Duration(trq.Step)) {
		found := false
		for j := range have {
			if j == 0 && i.Before(have[j].Start) {
				// our earliest datapoint in cache is after the first point the user wants
				fmt.Println("Optimization1")
				break
			}
			if i == have[j].Start || i == have[j].End || (i.After(have[j].Start) && have[j].End.After(i)) {
				found = true
				break
			}
		}
		if !found {
			fmt.Println("did not find", i)
			misses = append(misses, i)
		}
	}
	// Find the fill and gap ranges
	ins := []Extent{}
	e := time.Unix(0, 0)
	var inStart = e
	l := len(misses)
	for i := range misses {
		if inStart == e {
			inStart = misses[i]
		}
		if i+1 == l || misses[i+1] != misses[i].Add(time.Duration(trq.Step)*time.Second) {
			ins = append(ins, Extent{Start: inStart, End: misses[i]})
			fmt.Println("delta extent", ins[len(ins)-1])
			inStart = e
		}
	}
	return ins
}
