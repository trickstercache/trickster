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
	"fmt"
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
)

type stripKeysStubBackend struct {
	backends.Backend
	cfg *bo.Options
}

func (b *stripKeysStubBackend) Configuration() *bo.Options { return b.cfg }

func newStripKeysTargets(tb testing.TB, n int) pool.Targets {
	tb.Helper()
	targets := make(pool.Targets, n)
	for i := range n {
		labels := map[string]string{
			fmt.Sprintf("region_%d", i): fmt.Sprintf("us-east-%d", i),
			fmt.Sprintf("zone_%d", i):   fmt.Sprintf("az-%d", i),
			"cluster":                   fmt.Sprintf("c-%d", i),
		}
		be := &stripKeysStubBackend{
			cfg: &bo.Options{
				Name:       fmt.Sprintf("be-%d", i),
				Prometheus: &prop.Options{Labels: labels},
			},
		}
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(http.NotFoundHandler(), st, be)
	}
	return targets
}

// uncachedStripKeys reproduces the pre-D22 inline computation so the
// benchmark can quantify what the cache replaced.
func uncachedStripKeys(hl pool.Targets) []string {
	var keys []string
	seen := make(map[string]struct{})
	for _, t := range hl {
		if t == nil {
			continue
		}
		b := t.Backend()
		if b == nil {
			continue
		}
		cfg := b.Configuration()
		if cfg != nil && cfg.Prometheus != nil {
			for k := range cfg.Prometheus.Labels {
				if _, ok := seen[k]; !ok {
					seen[k] = struct{}{}
					keys = append(keys, k)
				}
			}
		}
	}
	return keys
}

func benchmarkStripKeysCached(b *testing.B, nTargets int) {
	targets := newStripKeysTargets(b, nTargets)
	h := &handler{}
	h.poolVersion.Add(1)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var sink []string
		for pb.Next() {
			sink = h.computeStripKeys(targets)
		}
		_ = sink
	})
}

func benchmarkStripKeysUncached(b *testing.B, nTargets int) {
	targets := newStripKeysTargets(b, nTargets)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var sink []string
		for pb.Next() {
			sink = uncachedStripKeys(targets)
		}
		_ = sink
	})
}

func BenchmarkStripKeysCached4(b *testing.B)    { benchmarkStripKeysCached(b, 4) }
func BenchmarkStripKeysCached8(b *testing.B)    { benchmarkStripKeysCached(b, 8) }
func BenchmarkStripKeysCached16(b *testing.B)   { benchmarkStripKeysCached(b, 16) }
func BenchmarkStripKeysUncached4(b *testing.B)  { benchmarkStripKeysUncached(b, 4) }
func BenchmarkStripKeysUncached8(b *testing.B)  { benchmarkStripKeysUncached(b, 8) }
func BenchmarkStripKeysUncached16(b *testing.B) { benchmarkStripKeysUncached(b, 16) }
