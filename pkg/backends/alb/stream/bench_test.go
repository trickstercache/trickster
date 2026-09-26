/*
 * Copyright 2026 The Trickster Authors
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
package stream

import (
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

const benchPoolSize = 8

// benchPool returns a started pool of n origin members; every other member is given weight
// when it is above 1
func benchPool(b *testing.B, name string, n, weight int) backends.Backend {
	b.Helper()
	members := make([]spec, n)
	for i := range members {
		w := 1
		if weight > 1 && i%2 == 0 {
			w = weight
		}
		members[i] = up(origin(b, name+"-"+strconv.Itoa(i), "10.0.0.1:"+strconv.Itoa(1000+i)), w)
	}
	return newALB(b, name, "rr", members...)
}

// benchmarkPick measures what a connection costs the relay: the pick, and the route's reports
func benchmarkPick(b *testing.B, u l4.Upstream) {
	b.Helper()
	benchmarkFlow(b, u, l4.Flow{})
}

func benchmarkFlow(b *testing.B, u l4.Upstream, f l4.Flow) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		r, ok := u.Pick(f)
		if !ok {
			b.Fatal("refused")
		}
		r.Dialed(0, nil)
		r.Closed(nil)
	}
}

func BenchmarkPoolUpstreamPickUniform(b *testing.B) {
	benchmarkPick(b, FromBackend(benchPool(b, "uniform", benchPoolSize, 1)))
}

func BenchmarkPoolUpstreamPickWeighted(b *testing.B) {
	benchmarkPick(b, FromBackend(benchPool(b, "weighted", benchPoolSize, 3)))
}

func BenchmarkPoolUpstreamPickNested(b *testing.B) {
	// a weighted outer pool over uniform inner pools, as a weighted rule over endpoints compiles to
	outer := make([]spec, 2)
	for i := range outer {
		outer[i] = up(benchPool(b, "inner"+strconv.Itoa(i), benchPoolSize, 1), 1+2*i)
	}
	benchmarkPick(b, FromBackend(newALB(b, "outer", "rr", outer...)))
}

func BenchmarkPoolUpstreamPickUniformParallel(b *testing.B) {
	u := FromBackend(benchPool(b, "uniform", benchPoolSize, 1))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if r, ok := u.Pick(l4.Flow{}); ok {
				r.Closed(nil)
			}
		}
	})
}

// a sticky pick: a client with a pin, one with none to find, and a flow with nothing to key on
func BenchmarkPoolUpstreamPickSticky(b *testing.B) {
	members := make([]spec, benchPoolSize)
	for i := range members {
		members[i] = up(origin(b, "sticky-"+strconv.Itoa(i), "10.0.0.1:"+strconv.Itoa(1000+i)), 1)
	}
	b.Cleanup(func() { sticky.ForgetTablesExcept(func(n string, _ *sticky.Table) bool { return n != "sticky" }) })
	u := FromBackend(newALBWith(b, "sticky", "rr", func(o *ao.Options) { o.Sticky = &so.Options{} }, members...))
	pinned := clientFlow(l4.ProtocolTCP, "198.51.100.1:5000", "")
	b.Run("hit", func(b *testing.B) { benchmarkFlow(b, u, pinned) })
	b.Run("keyless", func(b *testing.B) { benchmarkFlow(b, u, l4.Flow{}) })
}
