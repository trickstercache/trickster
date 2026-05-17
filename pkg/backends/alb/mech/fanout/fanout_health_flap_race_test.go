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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// TestAllRacesPerTargetHealthFlip races per-target hcStatus flips against
// in-flight fanout.All invocations. The assertion is data-race freedom under
// -race plus a minimum-progress check on both flipper and fanout sides.
func TestAllRacesPerTargetHealthFlip(t *testing.T) {
	t.Parallel()

	const numTargets = 6
	targets := make(pool.Targets, numTargets)
	for i := range numTargets {
		targets[i], _ = albpool.HealthyTarget(albpool.StatusHandler(http.StatusOK, ""))
	}

	parent := albpool.NewParentGET(t)
	ctx := context.Background()
	cfg := Config{Mechanism: "test"}

	res := albpool.RunHealthFlipRace(t, targets, func() int {
		results, _ := All(ctx, parent, targets, cfg)
		var ok int
		for _, r := range results {
			if !r.Failed {
				ok++
			}
		}
		return ok
	}, 2*time.Second, 50)

	if res.FanoutIters == 0 {
		t.Fatal("no fanout iterations completed")
	}
	if res.FlipperIters == 0 {
		t.Fatal("no flipper iterations completed")
	}
	if res.SucceededSlots == 0 {
		t.Fatal("no fanout slot succeeded; flapper starved every dispatch")
	}
}
