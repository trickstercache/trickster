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

package tsm

import (
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
)

func mkStripKeysTarget(labels map[string]string) *pool.Target {
	be := &stripKeysStubBackend{
		cfg: &bo.Options{Prometheus: &prop.Options{Labels: labels}},
	}
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	return pool.NewTarget(http.NotFoundHandler(), st, be)
}

func TestComputeStripKeysUnion(t *testing.T) {
	h := &handler{}
	h.poolVersion.Add(1)
	targets := pool.Targets{
		mkStripKeysTarget(map[string]string{"region": "us-east-1", "zone": "a"}),
		mkStripKeysTarget(map[string]string{"region": "us-west-2", "cluster": "c1"}),
		nil,
		mkStripKeysTarget(nil),
	}
	got := append([]string(nil), h.computeStripKeys(targets)...)
	sort.Strings(got)
	want := []string{"cluster", "region", "zone"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestComputeStripKeysCachedAcrossCalls(t *testing.T) {
	h := &handler{}
	h.poolVersion.Add(1)
	targets := pool.Targets{
		mkStripKeysTarget(map[string]string{"region": "us-east-1"}),
	}
	first := h.computeStripKeys(targets)
	second := h.computeStripKeys(targets)
	if &first[0] != &second[0] {
		t.Errorf("second call should return cached slice; got distinct backing arrays")
	}
}

func TestComputeStripKeysInvalidatesOnSetPool(t *testing.T) {
	h := &handler{}
	p1, _, _ := newSingleTargetPool(map[string]string{"region": "us-east-1"})
	h.SetPool(p1)
	defer p1.Stop()
	v1 := h.computeStripKeys(p1.Targets())
	if len(v1) != 1 || v1[0] != "region" {
		t.Fatalf("v1 unexpected: %v", v1)
	}

	p2, _, _ := newSingleTargetPool(map[string]string{"zone": "a", "cluster": "c1"})
	h.SetPool(p2)
	defer p2.Stop()
	v2 := h.computeStripKeys(p2.Targets())
	sort.Strings(v2)
	if len(v2) != 2 || v2[0] != "cluster" || v2[1] != "zone" {
		t.Fatalf("v2 unexpected after SetPool: %v", v2)
	}
}

func TestComputeStripKeysConcurrent(t *testing.T) {
	h := &handler{}
	h.poolVersion.Add(1)
	targets := pool.Targets{
		mkStripKeysTarget(map[string]string{"region": "us-east-1", "zone": "a"}),
		mkStripKeysTarget(map[string]string{"cluster": "c1"}),
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 64 {
				keys := h.computeStripKeys(targets)
				if len(keys) != 3 {
					t.Errorf("expected 3 keys, got %d: %v", len(keys), keys)
					return
				}
			}
		})
	}
	wg.Wait()
}

func newSingleTargetPool(labels map[string]string) (pool.Pool, []*pool.Target, []*healthcheck.Status) {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	be := &stripKeysStubBackend{
		cfg: &bo.Options{Prometheus: &prop.Options{Labels: labels}},
	}
	t := pool.NewTarget(http.NotFoundHandler(), st, be)
	p := pool.New(pool.Targets{t}, 0)
	p.RefreshHealthy()
	return p, []*pool.Target{t}, []*healthcheck.Status{st}
}

// newMultiTargetPool builds a pool with one target per labels map. The
// initialStatus slice (parallel to labelSets) controls which targets begin
// healthy; targets whose initial status is StatusFailing remain out of the
// live set returned by Pool.Targets() until their status is flipped.
func newMultiTargetPool(labelSets []map[string]string, initialStatus []int32) (pool.Pool, []*pool.Target, []*healthcheck.Status) {
	targets := make(pool.Targets, len(labelSets))
	statuses := make([]*healthcheck.Status, len(labelSets))
	for i, labels := range labelSets {
		st := &healthcheck.Status{}
		st.Set(initialStatus[i])
		be := &stripKeysStubBackend{
			cfg: &bo.Options{Prometheus: &prop.Options{Labels: labels}},
		}
		targets[i] = pool.NewTarget(http.NotFoundHandler(), st, be)
		statuses[i] = st
	}
	p := pool.New(targets, 0)
	p.RefreshHealthy()
	return p, targets, statuses
}

// TestComputeStripKeysUnpairedHealthyFlap reproduces the regression where a
// target unhealthy at first compute, then healthy on a later request (without
// SetPool bumping poolVersion), leaves its injected backend labels out of the
// cached stripKeys, so its series ship with un-stripped labels and split
// during dedup.
func TestComputeStripKeysUnpairedHealthyFlap(t *testing.T) {
	h := &handler{}
	p, _, st := newMultiTargetPool(
		[]map[string]string{
			{"region": "us-east-1"},
			{"zone": "a"},
		},
		[]int32{healthcheck.StatusPassing, healthcheck.StatusFailing},
	)
	h.SetPool(p)
	defer p.Stop()

	v1 := h.computeStripKeys(p.Targets())
	if len(v1) != 1 || v1[0] != "region" {
		t.Fatalf("v1 unexpected: %v", v1)
	}

	st[1].Set(healthcheck.StatusPassing)
	p.RefreshHealthy()
	live := p.Targets()
	if len(live) != 2 {
		t.Fatalf("expected both targets live after flip, got %d", len(live))
	}

	v2 := append([]string(nil), h.computeStripKeys(live)...)
	sort.Strings(v2)
	if len(v2) != 2 || v2[0] != "region" || v2[1] != "zone" {
		t.Fatalf("v2 missing labels from late-healthy target: %v", v2)
	}
}
