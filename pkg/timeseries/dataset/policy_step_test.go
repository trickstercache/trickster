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

package dataset

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/tinylib/msgp/msgp"
)

func TestCachePolicyStepRoundTrip(t *testing.T) {
	start := time.Unix(100, 0)
	ds := &DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{Step: time.Millisecond, PolicyStep: 15 * time.Second},
		ExtentList:     timeseries.ExtentList{{Start: start, End: start.Add(time.Hour - time.Millisecond)}},
	}
	b, err := MarshalDataSet(ds, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := UnmarshalDataSet(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded := ts.(*DataSet)
	if got := decoded.TimeRangeQuery.CachePolicyStep(); got != 15*time.Second {
		t.Errorf("cached policy step = %s, want 15s", got)
	}
	for name, candidate := range map[string]*DataSet{"original": ds, "cached": decoded, "clone": decoded.Clone().(*DataSet)} {
		t.Run(name, func(t *testing.T) {
			if got := candidate.Step(); got != time.Millisecond {
				t.Errorf("timestamp precision = %s, want 1ms", got)
			}
			if got := candidate.TimestampCount(); got != 240 {
				t.Errorf("retention timestamp count = %d, want 240", got)
			}
			candidate.CropToSize(1024, start.Add(2*time.Hour), timeseries.Extent{})
			if len(candidate.Extents()) != 1 {
				t.Fatalf("one hour of 15s policy points was evicted: %v", candidate.Extents())
			}
		})
	}
}

func TestCachePolicyStepLegacyDataSet(t *testing.T) {
	// Legacy cache objects have a raw step but no policy_step field.
	b := msgp.AppendMapHeader(nil, 1)
	b = msgp.AppendString(b, "trq")
	b = msgp.AppendMapHeader(b, 1)
	b = msgp.AppendString(b, "step")
	b = msgp.AppendInt64(b, int64(time.Second))
	ts, err := UnmarshalDataSet(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*DataSet)
	if ds.TimeRangeQuery.PolicyStep != 0 || ds.TimeRangeQuery.CachePolicyStep() != time.Second {
		t.Fatalf("legacy step fallback changed: %+v", ds.TimeRangeQuery)
	}
	ds.ExtentList = timeseries.ExtentList{{Start: time.Unix(1, 0), End: time.Unix(3, 0)}}
	if got := ds.TimestampCount(); got != 3 {
		t.Fatalf("legacy timestamp count = %d, want 3", got)
	}
}

func TestSizeCropperUsesPolicyStep(t *testing.T) {
	start := time.Unix(100, 0)
	el := timeseries.ExtentList{
		{Start: start, End: start.Add(999 * time.Millisecond), LastUsed: start},
		{Start: start.Add(time.Second), End: start.Add(1999 * time.Millisecond), LastUsed: start.Add(time.Second)},
		{Start: start.Add(2 * time.Second), End: start.Add(2999 * time.Millisecond), LastUsed: start.Add(2 * time.Second)},
	}
	ds := &DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{Step: time.Millisecond, PolicyStep: time.Second}, ExtentList: el.Clone()}
	ds.CropToSize(2, start.Add(time.Hour), el[2])
	if got := ds.Extents(); len(got) != 2 || got[0] != el[1] || got[1] != el[2] {
		t.Fatalf("LRU policy should evict only the first logical point, got %v", got)
	}
	if ds.Step() != time.Millisecond {
		t.Fatal("retention changed the raw timestamp precision")
	}
}
