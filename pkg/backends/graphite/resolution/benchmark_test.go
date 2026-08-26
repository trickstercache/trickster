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

package resolution_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
)

// a harness whose registry already holds every dev ladder: the steady state
// where renders resolve out of the registry without touching the origin
func warmResolver(tb testing.TB) *harness {
	h := newHarness(tb, nil)
	h.addAll()
	for leaf := range ladders {
		if _, err := h.learner.Learn(context.Background(), leaf, nil); err != nil {
			tb.Fatalf("%s: %v", leaf, err)
		}
	}
	// prime the wildcard expansion cache the same way a first render does
	if _, _, err := h.expander.Expand(context.Background(), "dev.fast.cpu.*.percent"); err != nil {
		tb.Fatal(err)
	}
	return h
}

func BenchmarkResolverRegistryHit(b *testing.B) {
	h := warmResolver(b)
	ctx := context.Background()
	cases := []struct {
		name string
		expr []string
		want resolution.Confidence
	}{
		{"single_leaf", []string{"dev.fast.cpu.host01.percent"}, resolution.Exact},
		// a wildcard whose expansion is cached: every leaf still resolves
		// out of the registry, but the target as a whole is Derived
		{"wildcard_cached", []string{"dev.fast.cpu.*.percent"}, resolution.Derived},
		// two leaves agreeing on a step: Exact is reserved for a single
		// leaf, so agreement across leaves reads as Derived
		{"two_leaves_one_ladder", []string{"dev.fast.cpu.host01.percent",
			"dev.fast.cpu.host02.percent"}, resolution.Derived},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				res := h.resolver.Resolve(ctx, tc.expr, 0, 30*time.Minute, false)
				if res.Confidence != tc.want {
					b.Fatalf("%v: confidence %v, want %v", tc.expr, res.Confidence, tc.want)
				}
			}
		})
	}
}

func TestResolverHotPathIsAllocationLight(t *testing.T) {
	// a registry hit must allocate a small fixed number of objects; growth
	// means per-call state (a map, closure, formatted string) crept in
	h := warmResolver(t)
	ctx := context.Background()
	const ceiling = 9 // measured at 7
	got := testing.AllocsPerRun(200, func() {
		if res := h.resolver.Resolve(ctx, []string{"dev.fast.cpu.host01.percent"},
			0, 30*time.Minute, false); res.Confidence != resolution.Exact {
			t.Fatalf("confidence %v, want exact", res.Confidence)
		}
	})
	if got > ceiling {
		t.Errorf("registry-hit resolution allocated %.0f objects, ceiling is %d", got, ceiling)
	}
	t.Logf("registry-hit resolution: %.0f allocations", got)
}

func TestResolveHasNoChannelHandoff(t *testing.T) {
	b, err := os.ReadFile("resolver.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, tok := range []string{"chan ", "<-", "select {", "go func"} {
		if strings.Contains(src, tok) {
			t.Errorf("resolver.go contains %q; the registry-hit path must stay synchronous", tok)
		}
	}
}

func BenchmarkResolverAtLadderScale(b *testing.B) {
	for _, n := range []int{4, 50_000} {
		b.Run(fmt.Sprintf("ladders_%d", n), func(b *testing.B) {
			now := time.Unix(1_787_350_000, 0)
			reg := resolution.NewRegistry(resolution.RegistryOptions{
				TTL: time.Hour, NegativeTTL: time.Second, MaxEntries: 100_000,
				Now: func() time.Time { return now },
			}, nil)
			for i := range n {
				// each ladder distinct (a distinct fingerprint) without
				// overflowing time.Duration at large i
				l, err := resolution.NewLadder([]resolution.Rung{
					{Step: 10 * time.Second, MaxAge: time.Hour + time.Duration(i)*10*time.Second},
					{Step: time.Minute, MaxAge: 720*time.Hour + time.Duration(i)*time.Minute},
				})
				if err != nil {
					b.Fatal(err)
				}
				leaf := fmt.Sprintf("scale.m%d.value", i)
				key, err := reg.SetLadder(leaf, l)
				if err != nil {
					b.Fatal(err)
				}
				if err := reg.SetLeaf(leaf, key, resolution.Exact); err != nil {
					b.Fatal(err)
				}
			}
			res := &resolution.Resolver{Registry: reg,
				Expander: &resolution.Expander{Registry: reg},
				Observer: newCounter()}
			ctx := context.Background()
			leaf := []string{"scale.m0.value"}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if r := res.Resolve(ctx, leaf, 0, 30*time.Minute, false); r.Confidence != resolution.Exact {
					b.Fatalf("confidence %v", r.Confidence)
				}
			}
		})
	}
}

func BenchmarkRegistryInsertAtLimit(b *testing.B) {
	const limit = 100_000
	now := time.Unix(1_787_350_000, 0)
	reg := resolution.NewRegistry(resolution.RegistryOptions{
		TTL: time.Hour, NegativeTTL: time.Second, MaxEntries: limit,
		Now: func() time.Time { return now },
	}, nil)
	l, err := resolution.NewLadder([]resolution.Rung{{Step: 10 * time.Second, MaxAge: 6 * time.Hour}})
	if err != nil {
		b.Fatal(err)
	}
	key, err := reg.SetLadder("seed", l)
	if err != nil {
		b.Fatal(err)
	}
	for i := range limit {
		if err := reg.SetLeaf(fmt.Sprintf("fill.%d", i), key, resolution.Exact); err != nil {
			b.Fatal(err)
		}
	}
	// concurrent readers observe any insertion stall directly
	stop := make(chan struct{})
	var maxRead atomic.Int64
	for range 4 {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				t0 := time.Now()
				reg.Leaf("fill.1")
				if d := time.Since(t0).Nanoseconds(); d > maxRead.Load() {
					maxRead.Store(d)
				}
			}
		}()
	}
	var maxInsert time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		t0 := time.Now()
		if err := reg.SetLeaf(fmt.Sprintf("over.%d", i), key, resolution.Exact); err != nil {
			b.Fatal(err)
		}
		if d := time.Since(t0); d > maxInsert {
			maxInsert = d
		}
	}
	b.StopTimer()
	close(stop)
	b.ReportMetric(float64(maxInsert.Nanoseconds()), "worst-insert-ns")
	b.ReportMetric(float64(maxRead.Load()), "worst-read-ns")
}
