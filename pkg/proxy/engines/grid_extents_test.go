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

package engines

import (
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestGridFetchExtent(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2024, 1, 1, h, m, 0, 0, time.UTC) }
	const backend, provider = "offgrid-fetch", "influxdb"
	rsc := &request.Resources{
		BackendOptions: &bo.Options{Name: backend, Provider: provider},
		TimeRangeQuery: &timeseries.TimeRangeQuery{Step: time.Hour, Phase: 30 * time.Minute},
	}
	counter := metrics.TimeseriesOffGridExtents.WithLabelValues(backend, provider)
	before := testutil.ToFloat64(counter)

	onGrid := timeseries.Extent{Start: at(1, 30), End: at(3, 30)}
	if got, ok := gridFetchExtent(onGrid, rsc); !ok || got != onGrid {
		t.Errorf("expected an on-grid extent unchanged, got %s", got)
	}
	if got := testutil.ToFloat64(counter) - before; got != 0 {
		t.Errorf("expected no off-grid count for an on-grid extent, got %v", got)
	}

	// the refetch from an epoch-grid backfill start that once split the 23:30 bucket
	got, ok := gridFetchExtent(timeseries.Extent{Start: at(0, 0), End: at(1, 30)}, rsc)
	if !ok || !got.Start.Equal(at(0, 30)) || !got.End.Equal(at(1, 30)) {
		t.Errorf("expected 00:30-01:30, got %s (%t)", got, ok)
	}
	if _, ok := gridFetchExtent(timeseries.Extent{Start: at(1, 40), End: at(2, 20)},
		rsc); ok {
		t.Error("expected no fetch for a range holding no whole bucket")
	}
	if got := testutil.ToFloat64(counter) - before; got != 2 {
		t.Errorf("expected 2 off-grid counts, got %v", got)
	}

	bare := &request.Resources{}
	e := timeseries.Extent{Start: at(0, 0), End: at(1, 0)}
	if got, ok := gridFetchExtent(e, bare); !ok || got != e {
		t.Error("expected no change without a time range query")
	}
}

func TestGridExtents(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2024, 1, 1, h, m, 0, 0, time.UTC) }
	rsc := &request.Resources{
		BackendOptions: &bo.Options{Name: "offgrid-coverage", Provider: "influxdb"},
		TimeRangeQuery: &timeseries.TimeRangeQuery{Step: time.Hour, Phase: 30 * time.Minute},
	}
	// coverage recorded as 22:30-23:00 leaves the 23:30 bucket in no delta; narrowing
	// it first makes 23:30 a gap again
	covered := gridExtents(timeseries.ExtentList{{Start: at(22, 30), End: at(23, 0)}}, rsc)
	gaps := covered.CalculateDeltas(timeseries.ExtentList{{
		Start: at(22, 30),
		End:   at(23, 30).Add(time.Hour),
	}}, time.Hour)
	if len(gaps) != 1 || !gaps[0].Start.Equal(at(23, 30)) {
		t.Errorf("expected a gap starting at 23:30, got %v", gaps)
	}
	if got := gridExtents(timeseries.ExtentList{}, &request.Resources{}); len(got) != 0 {
		t.Error("expected an empty list without a time range query")
	}
}

func TestTimeseriesChunksKeepEveryBucket(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name        string
		step, phase time.Duration
		start       time.Time
	}{
		{
			"phased hourly buckets", time.Hour, 30 * time.Minute,
			time.Date(2024, 1, 1, 0, 30, 0, 0, time.UTC),
		},
		{
			"weekly buckets on the epoch's Thursdays", 7 * day, 0,
			time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq := &timeseries.TimeRangeQuery{
				Step: test.step, Phase: test.phase,
				Extent: timeseries.Extent{Start: test.start, End: test.start.Add(40 * test.step)},
			}
			csize := 4 * test.step
			cext := timeseriesChunkExtent(trq, csize)
			// each chunk stores the buckets labeled from its start through its last step, so a
			// chunk start off the query's grid leaves a bucket in no chunk at every seam
			for c := cext.Start; c.Before(cext.End); c = c.Add(csize) {
				if !timeseries.OnGrid(c, test.step, test.phase) {
					t.Fatalf("chunk starting %s is off the grid; the bucket before its start "+
						"falls in no chunk", c)
				}
			}
			if cext.Start.After(trq.Extent.Start) || cext.End.Before(trq.Extent.End) {
				t.Errorf("chunks %s do not cover %s", cext, trq.Extent)
			}
		})
	}
}

func TestOldestRetained(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 9, 27, h, m, 0, 0, time.UTC) }
	tests := []struct {
		name                    string
		step, policyStep, phase time.Duration
		retention               int64
		now, expected           time.Time
	}{
		{
			"raw samples count whole policy steps", time.Millisecond, 5 * time.Minute, 0, 10,
			at(10, 33), at(9, 40),
		},
		{
			"bucket queries count whole buckets", 5 * time.Minute, 0, 0, 10,
			at(10, 33), at(9, 40),
		},
		{
			"phased buckets stay on their grid", time.Hour, 0, 30 * time.Minute, 3,
			at(12, 20), at(8, 30),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq := &timeseries.TimeRangeQuery{
				Step: test.step, PolicyStep: test.policyStep, Phase: test.phase,
			}
			if got := oldestRetained(trq, test.retention, test.now); !got.Equal(test.expected) {
				t.Errorf("expected %s got %s", test.expected, got)
			}
		})
	}
}
