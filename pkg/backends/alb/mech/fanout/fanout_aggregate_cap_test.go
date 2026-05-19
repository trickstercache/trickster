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

package fanout

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestAllRespectsAggregateCaptureCap verifies that MaxFanoutCaptureBytes caps
// the sum of in-flight capture allocations across all slots in a single
// fanout. With a wide pool and a generous per-slot cap, the worst case
// N*MaxCaptureBytes can dwarf available memory; the aggregate cap fail-fasts
// slots beyond the budget so the merge sees them as failures and existing
// partial-merge / 502 fallback paths handle them.
func TestAllRespectsAggregateCaptureCap(t *testing.T) {
	const (
		n            = 10
		perSlotBytes = 100 * 1024 // 100 KiB
		aggregateCap = 250 * 1024 // 250 KiB -- room for ~2 slots
	)
	big := bytes.Repeat([]byte("a"), perSlotBytes)

	targets := make(pool.Targets, n)
	for i := range n {
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(big)
		}))
	}

	parent := albpool.NewParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{
		Mechanism:             "test",
		MaxCaptureBytes:       perSlotBytes,
		MaxFanoutCaptureBytes: aggregateCap,
	})
	require.Len(t, results, n)

	var captured, failed, totalBytes int
	for i := range n {
		r := results[i]
		switch {
		case r.Failed:
			failed++
			if r.Capture != nil {
				totalBytes += len(r.Capture.Body())
			}
		default:
			captured++
			require.NotNil(t, r.Capture, "slot %d marked success but Capture is nil", i)
			require.Equal(t, perSlotBytes, len(r.Capture.Body()), "slot %d short body", i)
			totalBytes += len(r.Capture.Body())
		}
	}
	require.GreaterOrEqual(t, failed, n-3,
		"expected at least %d slots fail-fasted by aggregate cap; got %d captured, %d failed",
		n-3, captured, failed)
	require.LessOrEqual(t, captured, 3,
		"too many slots captured full bodies; aggregate cap not enforced")
	require.LessOrEqual(t, totalBytes, 3*perSlotBytes,
		"aggregate captured bytes %d exceeds bound", totalBytes)
}
