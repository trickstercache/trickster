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
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/testutil/stepwindow"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDeltaProxyCacheStepAlignmentGatesFastForward(t *testing.T) {
	const (
		supported = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
			timeseries.StepAlignmentPartialEnd
		step = 300 * time.Second
	)
	tests := []struct {
		name                 string
		configured, override timeseries.StepAlignment
		ffStatus             string
		fallback             bool
	}{
		{"the default partial_end runs fast forward", 0, 0, status.StatusKeyMiss, false},
		{"a configured truncate skips fast forward", timeseries.StepAlignmentTruncate, 0, statusOff, false},
		{
			"an override wins over the configured mode", timeseries.StepAlignmentTruncate,
			timeseries.StepAlignmentPartialEnd, status.StatusKeyMiss, false,
		},
		{
			"an unsupported override keeps the default", 0, timeseries.StepAlignmentDrop,
			status.StatusKeyMiss, true,
		},
	}
	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ts, _, r, rsc, err := setupTestHarnessDPC()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestHarness(ts, r)
			client := rsc.BackendClient.(*TestClient)
			client.stepAlignments, client.stepAlignment = supported, timeseries.StepAlignmentPartialEnd
			o := rsc.BackendOptions
			o.FastForwardDisable, o.StepAlignment = false, test.configured
			fallbacks := metrics.StepAlignmentFallbacks.WithLabelValues(o.Name,
				timeseries.StepAlignmentNameDrop, timeseries.StepAlignmentNamePartialEnd)
			before := testutil.ToFloat64(fallbacks)

			// fast forward needs the request to end at the engine's step-aligned now
			resp, ok := stepwindow.Retry(step, 3, func(attempt int, now time.Time) dpcResponse {
				client.InstantCacheKey = fmt.Sprintf("test-dpc-align-%d-%d-instant", i, attempt)
				client.RangeCacheKey = fmt.Sprintf("test-dpc-align-%d-%d-range", i, attempt)
				client.fftime = now.Truncate(time.Duration(o.PartialBucketTTL))
				u := r.URL
				u.Path = "/prometheus/api/v1/query_range"
				u.RawQuery = fmt.Sprintf("instantKey=%s&rangeKey=%s&step=%d&start=%d&end=%d&query=%s",
					client.InstantCacheKey, client.RangeCacheKey, int(step.Seconds()),
					now.Add(-time.Hour).Unix(), now.Unix(), queryReturnsOKNoLatency)
				req := r
				if test.override != 0 {
					req = r.WithContext(tctx.WithStepAlignment(r.Context(), test.override))
				}
				return serveDPC(client, req)
			})
			if !ok {
				t.Fatal("every attempt straddled a step boundary")
			}
			if err := testResultHeaderPartMatch(resp.header,
				map[string]string{keys.FFStatus: test.ffStatus}); err != nil {
				t.Error(err)
			}
			if got := testutil.ToFloat64(fallbacks) - before; (got > 0) != test.fallback {
				t.Errorf("fallbacks counted: %v, want a fallback: %v", got, test.fallback)
			}
		})
	}
}
