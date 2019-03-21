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
	"strconv"
	"testing"
	"time"
)

func TestNormalizeExtent(t *testing.T) {
	tests := []struct {
		start, end, stepMS, now int64
		rangeStart, rangeEnd    int64
		err                     bool
	}{
		// Basic test
		{
			1, 100, 1, 1,
			1, 100,
			false,
		},
		// Ensure that it aligns to the step interval
		{
			1, 103, 10, 1,
			0, 100,
			false,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			trq := TimeRangeQuery{Statement: "up", Extent: Extent{Start: time.Unix(test.start, 0), End: time.Unix(test.end, 0)}, Step: test.stepMS}

			trq.NormalizeExtent()

			if trq.Extent.Start.Unix() != test.rangeStart {
				t.Fatalf("Mismatch in rangeStart: expected=%d actual=%d", test.rangeStart, trq.Extent.Start.Unix())
			}
			if trq.Extent.End.Unix() != test.rangeEnd {
				t.Fatalf("Mismatch in rangeStart: expected=%d actual=%d", test.rangeEnd, trq.Extent.End.Unix())
			}
		})
	}
}
