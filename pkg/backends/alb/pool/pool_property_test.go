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

package pool

import (
	"net/http"
	"testing"

	"pgregory.net/rapid"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// Invariant: p.Targets() never returns a target whose current hcStatus
// is below the healthy floor, across any interleaving of status flips
// and refreshes.
func TestPropertyPoolTargetsRespectsFloor(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		const n = 4
		statuses := make([]*healthcheck.Status, n)
		targets := make(Targets, n)
		for i := range n {
			statuses[i] = &healthcheck.Status{}
			statuses[i].Set(healthcheck.StatusPassing)
			targets[i] = NewTarget(http.NotFoundHandler(), statuses[i], nil)
		}
		p := New(targets, int(healthcheck.StatusPassing)).(*pool)
		defer p.Stop()
		p.RefreshHealthy()

		expectedLive := func() map[*Target]bool {
			m := make(map[*Target]bool, n)
			for i, s := range statuses {
				if s.Get() >= healthcheck.StatusPassing {
					m[targets[i]] = true
				}
			}
			return m
		}

		invariant := func(rt *rapid.T) {
			got := p.Targets()
			want := expectedLive()
			for _, tgt := range got {
				if tgt == nil || tgt.hcStatus == nil {
					rt.Fatalf("Targets() returned nil-status entry: %v", tgt)
				}
				if tgt.hcStatus.Get() < healthcheck.StatusPassing {
					rt.Fatalf("Targets() returned target with status %d below floor",
						tgt.hcStatus.Get())
				}
				if !want[tgt] {
					rt.Fatalf("Targets() returned target not currently passing")
				}
			}
		}

		rt.Repeat(map[string]func(*rapid.T){
			"": invariant,
			"flip": func(t *rapid.T) {
				i := rapid.IntRange(0, n-1).Draw(t, "idx")
				up := rapid.Bool().Draw(t, "up")
				if up {
					statuses[i].Set(healthcheck.StatusPassing)
				} else {
					statuses[i].Set(healthcheck.StatusFailing)
				}
			},
			"refresh": func(_ *rapid.T) { p.RefreshHealthy() },
			"targets": func(_ *rapid.T) { _ = p.Targets() },
		})
	})
}

func TestPropertyPoolStopIdempotent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		const n = 3
		targets := make(Targets, n)
		for i := range n {
			st := &healthcheck.Status{}
			st.Set(healthcheck.StatusPassing)
			targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
		}
		p := New(targets, int(healthcheck.StatusPassing))
		stopCount := rapid.IntRange(1, 4).Draw(rt, "stopCount")
		for range stopCount {
			p.Stop()
		}
	})
}
