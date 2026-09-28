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

package nativedelta_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/vitess"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func roundedWindow(plan *sqlanalyzer.QueryPlan, now time.Time) nativedelta.Window {
	// BuildWindow's window before it planned from raw bounds, which ran open plans through now
	rawLower := plan.LowerBound.Value
	var rawUpper time.Time
	switch {
	case plan.UpperBound == nil:
		rawUpper = timeseries.FloorToGrid(now, plan.Step, plan.Phase).Add(plan.Step)
	case plan.UpperBound.Inclusive:
		rawUpper = plan.UpperBound.Value.Add(plan.Step)
	default:
		rawUpper = plan.UpperBound.Value
	}
	lower := timeseries.CeilToGrid(rawLower, plan.Step, plan.Phase)
	upper := timeseries.FloorToGrid(rawUpper, plan.Step, plan.Phase)
	if rawUpper.Sub(rawLower) < plan.Step || lower.After(upper) {
		upper = lower
	}
	window := nativedelta.Window{Lower: lower, Upper: upper}
	if lower.Equal(upper) {
		window.Output = timeseries.Extent{Start: lower, End: lower}
		window.Empty = true
		return window
	}
	window.Output = timeseries.Extent{Start: lower, End: upper.Add(-plan.Step)}
	window.Cacheable = timeseries.ExtentList{window.Output}
	return window
}

func sameWindow(a, b nativedelta.Window) bool {
	return a.Empty == b.Empty && a.Lower.Equal(b.Lower) && a.Upper.Equal(b.Upper) &&
		a.Output.Start.Equal(b.Output.Start) && a.Output.End.Equal(b.Output.End) &&
		len(a.Cacheable) == len(b.Cacheable)
}

func TestBuildWindowParity(t *testing.T) {
	const base = int64(1_699_999_200)
	past := time.Unix(base, 0).Add(30 * 24 * time.Hour)
	rfc := func(v int64) string { return time.Unix(v, 0).UTC().Format(time.RFC3339) }
	mysql := vitess.MustNewAnalyzer()
	datafusion := cockroach.NewAnalyzer(cockroach.Options{
		BucketMatchers: cockroach.DataFusionBucketMatchers(), RoundUnalignedTimeBounds: true,
	})
	var compared int
	check := func(a sqlanalyzer.DialectAnalyzer, query string, requireUpper bool) {
		t.Helper()
		got := a.Analyze(query, past)
		if got.Mode != sqlanalyzer.CacheModeDelta {
			return
		}
		want := roundedWindow(got.Plan, past)
		window, err := nativedelta.BuildWindow(got.Plan, past, requireUpper)
		if err != nil {
			return
		}
		compared++
		if !sameWindow(window, want) {
			t.Fatalf("%s:\nplanned %+v\nrounded %+v", query, window, want)
		}
	}
	for _, step := range []int64{60, 300} {
		offsets := []int64{0, 1, step / 2, step - 1}
		for _, lo := range offsets {
			for _, k := range []int64{0, 1, 2} {
				for _, uo := range offsets {
					lower, upper := base+lo, base+k*step+uo
					if upper < lower {
						continue
					}
					bucket := fmt.Sprintf("UNIX_TIMESTAMP(ts) DIV %d * %d AS time_sec", step, step)
					for _, where := range []string{
						fmt.Sprintf("ts >= FROM_UNIXTIME(%d) AND ts < FROM_UNIXTIME(%d)", lower, upper),
						fmt.Sprintf("ts >= FROM_UNIXTIME(%d) AND ts <= FROM_UNIXTIME(%d)", lower, upper),
						fmt.Sprintf("ts BETWEEN FROM_UNIXTIME(%d) AND FROM_UNIXTIME(%d)", lower, upper),
					} {
						check(mysql, fmt.Sprintf("SELECT %s, COUNT(*) AS value FROM events WHERE %s GROUP BY time_sec",
							bucket, where), true)
					}
					bin := fmt.Sprintf("date_bin(INTERVAL '%d seconds', time) AS b", step)
					for _, where := range []string{
						fmt.Sprintf("time >= %d AND time < %d", lower, upper),
						fmt.Sprintf("time >= %d AND time <= %d", lower, upper),
						fmt.Sprintf("b >= '%s' AND b <= '%s'", rfc(lower), rfc(upper)),
						fmt.Sprintf("b > '%s' AND b < '%s'", rfc(lower), rfc(upper)),
					} {
						check(datafusion, fmt.Sprintf("SELECT %s, avg(v) AS v FROM cpu WHERE %s GROUP BY 1",
							bin, where), false)
					}
				}
			}
		}
	}
	t.Logf("compared %d windows", compared)
	if compared < 300 {
		t.Fatalf("only %d windows were compared", compared)
	}

	// open-ended plans are the one intended difference: they now end before the bucket holding now
	now := time.Unix(base, 0).Add(90 * time.Second)
	got := datafusion.Analyze(fmt.Sprintf(
		"SELECT date_bin(INTERVAL '60 seconds', time) AS b, avg(v) AS v FROM cpu WHERE time >= %d GROUP BY 1",
		base-600), now)
	if got.Plan == nil {
		t.Fatalf("expected a delta plan: %v", got.Err)
	}
	window, err := nativedelta.BuildWindow(got.Plan, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if rounded := roundedWindow(got.Plan, now); !rounded.Output.End.Equal(time.Unix(base+60, 0)) ||
		!window.Output.End.Equal(time.Unix(base, 0)) {
		t.Fatalf("open-ended: planned %s, rounded %s", window.Output, rounded.Output)
	}
}
