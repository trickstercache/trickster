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
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestFanoutMetricLabels protects two operator-facing invariants:
//
//  1. ALBFanoutAttempts increments once per fanout invocation (not per slot),
//     so dashboards can compute failure rate as failures / attempts.
//  2. The mechanism label value flows through fanout unchanged, so renaming a
//     names.Mechanism* constant would break this test before it could silently
//     break dashboards.
func TestFanoutMetricLabels(t *testing.T) {
	const variant = ""
	mech := names.MechanismFR

	const n = 3
	targets := make(pool.Targets, n)
	for i := range n {
		targets[i], _ = albpool.Target(albpool.StatusHandler(http.StatusOK, "ok"))
	}

	parent := albpool.NewParentGET(t)
	albpool.RequireFanoutAttemptDelta(t, mech, variant, 1, func() {
		results, err := All(context.Background(), parent, targets, Config{Mechanism: mech, Variant: variant})
		require.NoError(t, err)
		require.Len(t, results, n)
	})

	require.Equal(t, "fr", names.MechanismFR, "fanout test asserts on the canonical short-name; constant drift would silently break dashboards")
}
