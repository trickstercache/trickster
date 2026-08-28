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

package aftership

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestBoundSemanticsMatrix(t *testing.T) {
	const prefix = "SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE "
	const suffix = " GROUP BY t"
	tests := []struct {
		name      string
		predicate string
		mode      sqlanalyzer.CacheMode
		start     int64
		end       int64
		rendered  []string
	}{
		{
			name: "raw half open", predicate: "ts >= 120 AND ts < 240", mode: sqlanalyzer.CacheModeDelta,
			start: 120, end: 180, rendered: []string{"ts >= 300", "ts < 420"},
		},
		{name: "raw strict lower", predicate: "ts > 120 AND ts < 240", mode: sqlanalyzer.CacheModeObject},
		{name: "raw inclusive upper", predicate: "ts >= 120 AND ts <= 240", mode: sqlanalyzer.CacheModeObject},
		{name: "raw between", predicate: "ts BETWEEN 120 AND 240", mode: sqlanalyzer.CacheModeObject},
		{name: "raw unaligned lower", predicate: "ts >= 121 AND ts < 240", mode: sqlanalyzer.CacheModeObject},
		{name: "raw unaligned upper", predicate: "ts >= 120 AND ts < 241", mode: sqlanalyzer.CacheModeObject},
		{
			name: "alias half open", predicate: "t >= 120 AND t < 240", mode: sqlanalyzer.CacheModeDelta,
			start: 120, end: 180, rendered: []string{"t >= 300", "t < 420"},
		},
		{
			name: "alias strict", predicate: "t > 120 AND t < 240", mode: sqlanalyzer.CacheModeDelta,
			start: 180, end: 180, rendered: []string{"t > 240", "t < 420"},
		},
		{
			name: "alias inclusive", predicate: "t >= 120 AND t <= 240", mode: sqlanalyzer.CacheModeDelta,
			start: 120, end: 240, rendered: []string{"t >= 300", "t <= 360"},
		},
		{
			name: "alias between", predicate: "t BETWEEN 120 AND 240", mode: sqlanalyzer.CacheModeDelta,
			start: 120, end: 240, rendered: []string{"t BETWEEN 300 AND 360"},
		},
		{
			name: "alias unaligned", predicate: "t >= 121 AND t < 241", mode: sqlanalyzer.CacheModeDelta,
			start: 180, end: 240, rendered: []string{"t >= 300", "t < 420"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer().Analyze(prefix+test.predicate+suffix, time.Unix(500, 0))
			if analysis.Mode != test.mode {
				t.Fatalf("mode = %v, want %v (reason=%v, err=%v)",
					analysis.Mode, test.mode, analysis.Reason, analysis.Err)
			}
			if test.mode != sqlanalyzer.CacheModeDelta {
				if analysis.Reason != sqlanalyzer.ReasonUnsafePredicate {
					t.Errorf("reason = %v, want unsafe predicate", analysis.Reason)
				}
				return
			}

			extent := analysis.Plan.RequestExtent(time.Unix(500, 0))
			if extent.Start.Unix() != test.start || extent.End.Unix() != test.end {
				t.Errorf("extent = [%d,%d], want [%d,%d]", extent.Start.Unix(), extent.End.Unix(), test.start, test.end)
			}
			rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
				Start: time.Unix(300, 0), End: time.Unix(360, 0),
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.rendered {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered query lacks %q: %s", want, rendered)
				}
			}
		})
	}
}

func TestHalfOpenRawBoundsRenderFullPartialAndShardedMisses(t *testing.T) {
	analysis := NewAnalyzer().Analyze(
		"SELECT toStartOfMinute(ts) AS t, count() FROM events "+
			"WHERE ts >= 120 AND ts < 240 GROUP BY t",
		time.Unix(500, 0),
	)
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}
	tests := []struct {
		name   string
		extent timeseries.Extent
		want   []string
	}{
		{
			name: "full miss", extent: timeseries.Extent{Start: time.Unix(120, 0), End: time.Unix(180, 0)},
			want: []string{"ts >= 120", "ts < 240"},
		},
		{
			name: "left shard", extent: timeseries.Extent{Start: time.Unix(120, 0), End: time.Unix(120, 0)},
			want: []string{"ts >= 120", "ts < 180"},
		},
		{
			name:   "right shard includes interior boundary",
			extent: timeseries.Extent{Start: time.Unix(180, 0), End: time.Unix(180, 0)},
			want:   []string{"ts >= 180", "ts < 240"},
		},
		{
			name: "later partial miss", extent: timeseries.Extent{Start: time.Unix(300, 0), End: time.Unix(360, 0)},
			want: []string{"ts >= 300", "ts < 420"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := analysis.Plan.RenderExtent(test.extent)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered query lacks %q: %s", want, rendered)
				}
			}
		})
	}
}

func TestGroupByResolvesSelectResultShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		mode sqlanalyzer.CacheMode
		tags []string
	}{
		{
			name: "selected source and alias",
			body: "SELECT toStartOfMinute(ts) AS t, service AS svc, host, count() AS cnt " +
				"FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t, service, host",
			mode: sqlanalyzer.CacheModeDelta, tags: []string{"svc", "host"},
		},
		{
			name: "group by selected alias",
			body: "SELECT toStartOfMinute(ts) AS t, service AS svc, count() " +
				"FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t, svc",
			mode: sqlanalyzer.CacheModeDelta, tags: []string{"svc"},
		},
		{
			name: "quoted identifiers",
			body: "SELECT toStartOfMinute(`ts`) AS `bucket`, `service` AS `svc`, count() " +
				"FROM events WHERE `ts` >= 120 AND `ts` < 240 GROUP BY `bucket`, `svc`",
			mode: sqlanalyzer.CacheModeDelta, tags: []string{"svc"},
		},
		{
			name: "qualified identifiers",
			body: "SELECT toStartOfMinute(events.ts) AS t, events.service AS svc, count() " +
				"FROM events WHERE events.ts >= 120 AND events.ts < 240 GROUP BY t, events.service",
			mode: sqlanalyzer.CacheModeDelta, tags: []string{"svc"},
		},
		{
			name: "missing timestamp group",
			body: "SELECT toStartOfMinute(ts) AS t, service, count() FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY service",
			mode: sqlanalyzer.CacheModeObject,
		},
		{
			name: "unselected group",
			body: "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY t, service",
			mode: sqlanalyzer.CacheModeObject,
		},
		{
			name: "selected tag not grouped",
			body: "SELECT toStartOfMinute(ts) AS t, service, count() FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY t",
			mode: sqlanalyzer.CacheModeObject,
		},
		{
			name: "aggregate alias is not tag",
			body: "SELECT toStartOfMinute(ts) AS t, count() AS cnt FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY t, cnt",
			mode: sqlanalyzer.CacheModeObject,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer().Analyze(test.body, time.Unix(500, 0))
			if analysis.Mode != test.mode {
				t.Fatalf("mode = %v, want %v (reason=%v, err=%v)",
					analysis.Mode, test.mode, analysis.Reason, analysis.Err)
			}
			if test.mode == sqlanalyzer.CacheModeDelta && !equalStrings(analysis.Plan.GroupColumns, test.tags) {
				t.Errorf("tags = %v, want %v", analysis.Plan.GroupColumns, test.tags)
			}
			if test.mode == sqlanalyzer.CacheModeObject && analysis.Reason != sqlanalyzer.ReasonUnsupportedGrouping {
				t.Errorf("reason = %v, want unsupported grouping", analysis.Reason)
			}
		})
	}
}

// TestTimezoneExpressionsFailClosed proves that timezone-aware bucket and
// bound expressions are never delta-cached: cadence and phase cannot be
// verified against Trickster's UTC extent convention, so each form must fall
// back to the object cache with a stable reason code.
func TestTimezoneExpressionsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		reason sqlanalyzer.AnalysisReason
	}{
		{
			name: "toStartOfInterval with timezone argument",
			body: "SELECT toStartOfInterval(ts, INTERVAL 1 minute, 'America/Denver') AS t, count() " +
				"FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t",
			reason: sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			name: "fixed bucket function with timezone argument",
			body: "SELECT toStartOfHour(ts, 'America/Denver') AS t, count() " +
				"FROM events WHERE ts >= 0 AND ts < 7200 GROUP BY t",
			reason: sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			name: "toTimeZone wrapped source column",
			body: "SELECT toStartOfMinute(toTimeZone(ts, 'UTC')) AS t, count() " +
				"FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t",
			reason: sqlanalyzer.ReasonUnsupportedBucket,
		},
		{
			name: "timezone-qualified toDateTime bounds",
			body: "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
				"WHERE ts >= toDateTime(120, 'America/Denver') AND ts < toDateTime(240, 'America/Denver') GROUP BY t",
			reason: sqlanalyzer.ReasonNotTimeRange,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer().Analyze(test.body, time.Unix(500, 0))
			if analysis.Mode != sqlanalyzer.CacheModeObject {
				t.Fatalf("mode = %v, want object (reason=%v, err=%v)",
					analysis.Mode, analysis.Reason, analysis.Err)
			}
			if analysis.Reason != test.reason {
				t.Errorf("reason = %v, want %v (err=%v)", analysis.Reason, test.reason, analysis.Err)
			}
			if analysis.Plan != nil {
				t.Error("timezone-aware query must not produce a delta-cache plan")
			}
		})
	}
}

func TestFloatingEpochBoundsFailClosed(t *testing.T) {
	query := "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
		"WHERE ts >= 120.5 AND ts < 240.5 GROUP BY t"
	analysis := NewAnalyzer().Analyze(query, time.Now())
	if analysis.Mode != sqlanalyzer.CacheModeObject {
		t.Fatalf("floating epoch bounds produced a delta-cache plan: %+v", analysis)
	}
}

func TestRendererPrivatePlaceholdersAreCollisionSafe(t *testing.T) {
	query := "SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE note = '<$TRICKSTER_TS1_0$>' " +
		"AND marker = '<$TS1$>' AND ts >= 120 AND ts < 240 GROUP BY t"
	analysis := NewAnalyzer().Analyze(query, time.Unix(500, 0))
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}
	if !strings.Contains(analysis.Plan.CanonicalSQL, "note = '<$TRICKSTER_TS1_0$>'") ||
		!strings.Contains(analysis.Plan.CanonicalSQL, "marker = '<$TS1$>'") {
		t.Fatalf("canonicalization modified user literals: %s", analysis.Plan.CanonicalSQL)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{Start: time.Unix(300, 0), End: time.Unix(360, 0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"note = '<$TRICKSTER_TS1_0$>'", "marker = '<$TS1$>'", "ts >= 300", "ts < 420"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered query lacks %q: %s", want, rendered)
		}
	}
}

func TestRendererIsConcurrentAndImmutable(t *testing.T) {
	const workers = 64
	analysis := NewAnalyzer().Analyze(
		"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t",
		time.Unix(500, 0),
	)
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}

	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			start := int64(600 + i*120)
			results[i], errs[i] = analysis.Plan.RenderExtent(timeseries.Extent{
				Start: time.Unix(start, 0), End: time.Unix(start+60, 0),
			})
		})
	}
	wg.Wait()

	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("render %d: %v", i, errs[i])
		}
		start := int64(600 + i*120)
		for _, want := range []string{"ts >= " + strconv.FormatInt(start, 10), "ts < " + strconv.FormatInt(start+120, 10)} {
			if !strings.Contains(results[i], want) {
				t.Errorf("render %d lacks %q: %s", i, want, results[i])
			}
		}
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
