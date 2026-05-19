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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestAllRoutingFlapAttributesFailure verifies that when a target was healthy
// at LiveTargets-snapshot time but has since flipped to Failing, a fanout
// failure against that target is attributed to reason="routing_flap" rather
// than a generic transport-error reason. This prevents health flap from
// inflating real upstream-failure dashboards/alerts.
func TestAllRoutingFlapAttributesFailure(t *testing.T) {
	const mechName = "test-flap"
	const maxBytes = 128

	st := healthcheck.NewStatus("flap", "", "", healthcheck.StatusPassing, time.Time{}, nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxBytes*4))
	})
	target := pool.NewTarget(handler, st, nil)

	p := pool.New(pool.Targets{target}, int(healthcheck.StatusPassing))
	defer p.Stop()
	p.RefreshHealthy()

	live := p.Targets()
	require.Len(t, live, 1, "live targets should include the passing target")

	st.Set(healthcheck.StatusFailing)

	parent := albpool.NewParentGET(t)
	albpool.RequireFanoutFailureDelta(t, mechName, "", "routing_flap", 1, func() {
		albpool.RequireFanoutFailureDelta(t, mechName, "", "truncated", 0, func() {
			results, _ := All(context.Background(), parent, live, Config{Mechanism: mechName, MaxCaptureBytes: maxBytes})
			require.Len(t, results, 1)
			require.True(t, results[0].Failed, "slot must surface failure")
		})
	})
}
