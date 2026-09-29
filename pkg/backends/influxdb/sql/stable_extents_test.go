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

package sql

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestOriginPhasedRefetchStartsOnBucket(t *testing.T) {
	// an open-ended date_bin on a 30m-phased hourly grid: the refetch after the volatile tail is
	// trimmed begins at a bucket boundary and ends before the live bucket
	now := time.Date(2024, 1, 2, 1, 0, 0, 0, time.UTC)
	analysis := Analyzer().Analyze(`SELECT date_bin(INTERVAL '1 hour', time, `+
		`TIMESTAMP '2024-01-01 00:30:00') AS time, sum(v) FROM t `+
		`WHERE time >= TIMESTAMP '2024-01-01 22:30:00' GROUP BY 1`, now)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		t.Fatalf("expected a delta plan, got %v (%v)", analysis.Mode, analysis.Err)
	}
	plan := analysis.Plan
	if plan.Phase != 30*time.Minute {
		t.Fatalf("expected a 30m phase, got %s", plan.Phase)
	}
	window, err := nativedelta.BuildWindow(plan, now, false, timeseries.StepAlignmentDrop)
	if err != nil {
		t.Fatal(err)
	}
	stable := nativedelta.StableExtents(window.Cacheable, plan.Step, plan.Phase, plan.Step, now)
	gaps := stable.CalculateDeltas(timeseries.ExtentList{window.Output}, plan.Step)
	if len(gaps) != 1 {
		t.Fatalf("expected one gap, got %v", gaps)
	}
	rendered, err := plan.RenderExtent(gaps[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"time" >= TIMESTAMP '2024-01-01 23:30:00'`,
		`"time" < TIMESTAMP '2024-01-02 00:30:00'`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("expected %s in %s", want, rendered)
		}
	}
}
