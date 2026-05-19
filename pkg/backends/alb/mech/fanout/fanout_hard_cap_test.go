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
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestAllHardCapsOnOversizedBodies verifies that even when every upstream
// returns far more bytes than the per-slot MaxCaptureBytes, the sum of
// captured bytes across all admitted slots stays within MaxFanoutCaptureBytes.
// This is the hard-cap guarantee: per-writer truncation bounds each admitted
// slot, and aggregate-budget admission bounds the slot count.
func TestAllHardCapsOnOversizedBodies(t *testing.T) {
	const (
		n            = 100
		perSlotBytes = 8 * 1024  // 8 KiB per slot
		aggregateCap = 24 * 1024 // 24 KiB aggregate, admits 3 slots
		oversize     = 64 * 1024 // upstreams each return 8x the per-slot cap
	)
	big := bytes.Repeat([]byte("z"), oversize)

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
		if r.Capture != nil {
			body := r.Capture.Body()
			require.LessOrEqualf(t, len(body), perSlotBytes,
				"slot %d captured %d bytes, exceeds per-slot cap %d",
				i, len(body), perSlotBytes)
			totalBytes += len(body)
		}
		if r.Failed {
			failed++
			continue
		}
		captured++
	}

	require.LessOrEqual(t, totalBytes, aggregateCap,
		"aggregate captured bytes %d exceeds MaxFanoutCaptureBytes %d",
		totalBytes, aggregateCap)
	require.LessOrEqual(t, captured, aggregateCap/perSlotBytes,
		"too many slots captured; aggregate cap not enforced")
	require.GreaterOrEqual(t, failed, n-(aggregateCap/perSlotBytes),
		"expected fail-fast for slots beyond budget")
}

// TestAllHardCapDefaultsPerSlotReserveWhenMaxCaptureBytesUnset verifies that
// leaving Config.MaxCaptureBytes at zero does not silently bypass the
// aggregate cap. Without the default, perSlotReserve would be 0 and every
// slot would admit, leaving MaxFanoutCaptureBytes a no-op.
func TestAllHardCapDefaultsPerSlotReserveWhenMaxCaptureBytesUnset(t *testing.T) {
	const n = 4
	// aggregate cap small enough that one slot at DefaultMaxBytes admits and
	// the rest fail-fast. capture.DefaultMaxBytes is 256 MiB; setting the
	// aggregate cap to one of those means slots 1..n-1 must fail.
	aggregate := capture.DefaultMaxBytes

	targets := make(pool.Targets, n)
	for i := range n {
		targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
	}
	parent := albpool.NewParentGET(t)
	results, _ := All(context.Background(), parent, targets, Config{
		Mechanism:             "test",
		MaxFanoutCaptureBytes: aggregate,
	})
	require.Len(t, results, n)

	var failed int
	for _, r := range results {
		if r.Failed {
			failed++
		}
	}
	require.Equal(t, n-1, failed,
		"expected %d fail-fast slots when per-slot reserve defaults to DefaultMaxBytes; got %d",
		n-1, failed)
}
