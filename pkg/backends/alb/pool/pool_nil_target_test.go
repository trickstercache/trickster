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

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

// New must skip nil entries in the targets slice rather than dereffing
// t.hcStatus on them.
func TestPoolNilTargetDoesNotPanicOnNew(t *testing.T) {
	valid := func() *Target {
		return NewTarget(http.NotFoundHandler(), &healthcheck.Status{}, nil)
	}

	cases := []struct {
		name      string
		targets   Targets
		wantValid int // expected count of non-nil targets in LiveTargets after registration
	}{
		{name: "single nil", targets: Targets{nil}, wantValid: 0},
		{name: "multi nil", targets: Targets{nil, nil}, wantValid: 0},
		{name: "mixed nil and valid", targets: Targets{nil, valid()}, wantValid: 1},
		{name: "valid then nil", targets: Targets{valid(), nil}, wantValid: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("pool.New panicked on nil target(s): %v", r)
				}
			}()

			p := New(tc.targets, -1)
			defer p.Stop()

			// mark every non-nil target as passing so it lands in LiveTargets.
			for _, tt := range tc.targets {
				if tt != nil {
					tt.hcStatus.Set(healthcheck.StatusPassing)
				}
			}
			p.RefreshHealthy()

			live := p.Targets()
			if len(live) != tc.wantValid {
				t.Errorf("LiveTargets: expected %d valid, got %d", tc.wantValid, len(live))
			}
			for i, tgt := range live {
				if tgt == nil {
					t.Errorf("LiveTargets[%d] is nil; nil entries should be filtered", i)
				}
			}
		})
	}
}
