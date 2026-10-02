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
	"context"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestApplyDirectives(t *testing.T) {
	for _, test := range []struct {
		name       string
		directives timeseries.Directives
		window     time.Duration
		ffDisable  bool
	}{
		{"none", timeseries.Directives{}, time.Minute, false},
		{
			"a volatile window replaces the provider's",
			timeseries.Directives{VolatileWindow: time.Second},
			time.Second, false,
		},
		{"fast forward off", timeseries.Directives{FastForwardDisable: true}, time.Minute, true},
		{
			"a mode decides fast forward instead",
			timeseries.Directives{FastForwardDisable: true, StepAlignment: timeseries.StepAlignmentPartialEnd},
			time.Minute, false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			trq := &timeseries.TimeRangeQuery{VolatileWindow: time.Minute, Directives: test.directives}
			rlo := &timeseries.RequestOptions{}
			applyDirectives(trq, rlo)
			if trq.VolatileWindow != test.window || rlo.FastForwardDisable != test.ffDisable {
				t.Errorf("window %s, fast forward disabled %t", trq.VolatileWindow, rlo.FastForwardDisable)
			}
			// a provider that returns no request options
			applyDirectives(trq, nil)
		})
	}
}

func TestResolveStepAlignmentCountsADirectiveTheALBOverrides(t *testing.T) {
	const partialEnd, truncate = timeseries.StepAlignmentPartialEnd, timeseries.StepAlignmentTruncate
	o := &bo.Options{Name: "directive-fallback"}
	fallbacks := metrics.StepAlignmentFallbacks.WithLabelValues(o.Name, partialEnd.String(), truncate.String())
	for _, test := range []struct {
		name    string
		allowed timeseries.StepAlignment
		want    timeseries.StepAlignment
		counted float64
	}{
		{"a member lacks the directive's mode", truncate, truncate, 1},
		{"every member supports it", truncate | partialEnd, partialEnd, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			trq := &timeseries.TimeRangeQuery{
				StepAlignments: truncate | partialEnd, StepAlignment: truncate,
				Directives: timeseries.Directives{StepAlignment: partialEnd},
			}
			ctx := tctx.WithStepAlignmentOverride(context.Background(),
				&tctx.StepAlignmentOverride{Mode: truncate, Allowed: test.allowed})
			before := testutil.ToFloat64(fallbacks)
			resolveStepAlignment(ctx, o, trq, nil, nil)
			if trq.StepAlignment != test.want || testutil.ToFloat64(fallbacks)-before != test.counted {
				t.Errorf("resolved %s, counted %v", trq.StepAlignment, testutil.ToFloat64(fallbacks)-before)
			}
		})
	}
}
