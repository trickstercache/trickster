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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func TestLiveTargets_ReflectsImmediateFlap(t *testing.T) {
	const n = 8
	targets := make(Targets, n)
	for i := range n {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
	}
	p := New(targets, 1)
	defer p.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Targets()) == n {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := len(p.Targets()); got != n {
		t.Fatalf("setup: expected %d live targets, got %d", n, got)
	}

	targets[0].hcStatus.Set(healthcheck.StatusFailing)

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Targets()) == n-1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("LiveTargets did not reflect flap within 1s; got %d expected %d",
		len(p.Targets()), n-1)
}
