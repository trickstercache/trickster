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

package l4

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

const benchPoolSize = 8

func benchOrigin(b *testing.B, name, addr string) backends.Backend {
	b.Helper()
	o := bo.New()
	o.OriginURL = "tcp://" + addr
	if err := o.Initialize(name); err != nil {
		b.Fatal(err)
	}
	be, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
	if err != nil {
		b.Fatal(err)
	}
	return be
}

// benchPooled returns a pool holder of n origin members; every other member is given weight
// when it is above 1
func benchPooled(b *testing.B, name string, n, weight int) backends.Backend {
	b.Helper()
	members := make(pool.Targets, n)
	for i := range members {
		w := 1
		if weight > 1 && i%2 == 0 {
			w = weight
		}
		members[i] = member(benchOrigin(b, name+"-"+strconv.Itoa(i), "10.0.0.1:"+strconv.Itoa(1000+i)),
			w, healthcheck.StatusPassing)
	}
	return benchHolder(b, name, members)
}

func benchHolder(b *testing.B, name string, members pool.Targets) backends.Backend {
	b.Helper()
	be, err := backends.New(name, bo.New(), nil, http.NotFoundHandler(), nil)
	if err != nil {
		b.Fatal(err)
	}
	p := pool.New(members, int(healthcheck.StatusUnchecked))
	b.Cleanup(p.Stop)
	return &pooledBackend{Backend: be, p: p}
}

func benchmarkAddr(b *testing.B, up Upstream) {
	b.Helper()
	// the first read of each pool builds its cached member list; measure past that
	deadline := time.Now().Add(2 * time.Second)
	for testing.AllocsPerRun(1, func() { up.Addr() }) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, ok := up.Addr(); !ok {
			b.Fatal("refused")
		}
	}
}

func BenchmarkPoolUpstreamAddrUniform(b *testing.B) {
	benchmarkAddr(b, FromBackend(benchPooled(b, "uniform", benchPoolSize, 1)))
}

func BenchmarkPoolUpstreamAddrWeighted(b *testing.B) {
	benchmarkAddr(b, FromBackend(benchPooled(b, "weighted", benchPoolSize, 3)))
}

func BenchmarkPoolUpstreamAddrNested(b *testing.B) {
	// a weighted outer pool over uniform inner pools, as a weighted rule over endpoints compiles to
	outer := make(pool.Targets, 2)
	for i := range outer {
		outer[i] = member(benchPooled(b, "inner"+strconv.Itoa(i), benchPoolSize, 1),
			1+2*i, healthcheck.StatusPassing)
	}
	benchmarkAddr(b, FromBackend(benchHolder(b, "outer", outer)))
}

func BenchmarkPoolUpstreamAddrUniformParallel(b *testing.B) {
	up := FromBackend(benchPooled(b, "uniform", benchPoolSize, 1))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			up.Addr()
		}
	})
}
