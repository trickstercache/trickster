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

func BenchmarkLiveTargets(b *testing.B) {
	const n = 50
	targets := make(Targets, n)
	for i := range n {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = NewTarget(http.NotFoundHandler(), st, nil)
	}
	p := &pool{targets: targets, healthyFloor: 1}
	p.RefreshHealthy()
	if got := len(p.Targets()); got != n {
		b.Fatalf("setup: expected %d healthy targets, got %d", n, got)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var sink Targets
		for pb.Next() {
			sink = p.Targets()
		}
		_ = sink
	})
}
