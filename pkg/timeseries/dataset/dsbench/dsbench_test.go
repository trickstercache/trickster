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

package dsbench

import (
	"math"
	"os"
	"runtime"
	"runtime/metrics"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	envEnable       = "TRICKSTER_DSBENCH"
	envBudgetMB     = "TRICKSTER_DSBENCH_BUDGET_MB"
	envLoadDuration = "TRICKSTER_DSBENCH_LOAD"

	defaultBudgetMB     = 512
	defaultLoadDuration = 3 * time.Second
	forcedGCs           = 8
	loadNodes           = 256
	loadBufferBytes     = 16 << 10

	metricGCCPU       = "/cpu/classes/gc/total:cpu-seconds"
	metricAssistCPU   = "/cpu/classes/gc/mark/assist:cpu-seconds"
	metricTotalCPU    = "/cpu/classes/total:cpu-seconds"
	metricGCCycles    = "/gc/cycles/total:gc-cycles"
	metricHeapObjects = "/gc/heap/objects:objects"
)

func BenchmarkBuild(b *testing.B) {
	for _, s := range shapes() {
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = s.build()
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(s.pointCount()), "ns/point")
		})
	}
}

func BenchmarkCacheMarshal(b *testing.B) {
	for _, s := range shapes() {
		ts := s.build()
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			var n int
			for b.Loop() {
				body, err := s.marshal(ts, nil, 0)
				if err != nil {
					b.Fatal(err)
				}
				n = len(body)
			}
			b.ReportMetric(float64(n)/float64(s.pointCount()), "bytes/point")
		})
	}
}

func BenchmarkCacheUnmarshal(b *testing.B) {
	for _, s := range shapes() {
		body, err := s.marshal(s.build(), nil, 0)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if _, err := s.unmarshal(body, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestRetainedHeap fills the heap with each shape, as a memory cache holds its entries, then
// measures what keeping them live costs: heap per point, forced GC time, and GC share under load.
func TestRetainedHeap(t *testing.T) {
	if os.Getenv(envEnable) == "" {
		t.Skip("set " + envEnable + "=1 to run the retained heap measurements")
	}
	budget := int64(envInt(envBudgetMB, defaultBudgetMB)) << 20
	load := defaultLoadDuration
	if v := os.Getenv(envLoadDuration); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		load = d
	}
	t.Logf("budget %d MiB, load %s, GOMAXPROCS %d, GOGC %s", budget>>20, load,
		runtime.GOMAXPROCS(0), os.Getenv("GOGC"))

	baseGCMS := forcedGCMillis()
	base := runLoad(load)
	t.Logf("%-20s %7s %8s %8s %10s %9s %8s %9s %8s %8s %8s %8s %8s",
		"shape", "copies", "live MiB", "B/point", "objs/point", "size/heap", "gc ms", "gc ms/GiB",
		"gc cpu%", "assist%", "cycles/s", "p50 µs", "p99 µs")
	t.Logf("%-20s %7s %8s %8s %10s %9s %8.2f %9s %8.2f %8.2f %8.1f %8.1f %8.1f", "(empty heap)",
		"-", "-", "-", "-", "-", baseGCMS, "-", base.gcCPU*100, base.assistCPU*100, base.cyclesPerSec,
		base.p50, base.p99)
	for _, s := range shapes() {
		r := retain(s, budget)
		gcMS := forcedGCMillis()
		l := runLoad(load)
		t.Logf("%-20s %7d %8.0f %8.1f %10.2f %9.2f %8.2f %9.2f %8.2f %8.2f %8.1f %8.1f %8.1f", s.name,
			len(r.kept), r.liveBytes/(1<<20), r.bytesPerPoint, r.objectsPerPoint, r.sizeRatio, gcMS,
			gcMS/(r.liveBytes/(1<<30)), l.gcCPU*100, l.assistCPU*100, l.cyclesPerSec, l.p50, l.p99)
		runtime.KeepAlive(r.kept)
	}
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

type retained struct {
	kept            []timeseries.Timeseries
	liveBytes       float64
	bytesPerPoint   float64
	objectsPerPoint float64
	// sizeRatio is what Size() reports against the live heap the entries hold
	sizeRatio float64
}

// retain keeps copies of s live until they hold about budget bytes of heap: it measures one copy's
// live size after a collection, then builds as many more as the budget needs
func retain(s shape, budget int64) retained {
	runtime.GC()
	before := heapStats()
	r := retained{kept: []timeseries.Timeseries{s.build()}}
	runtime.GC()
	perCopy := max(int64(heapStats().alloc)-int64(before.alloc), 1)
	n := max(int((budget+perCopy-1)/perCopy), 1)
	for len(r.kept) < n {
		r.kept = append(r.kept, s.build())
	}
	var reported int64
	for _, ts := range r.kept {
		reported += ts.Size()
	}
	runtime.GC()
	after := heapStats()
	points := float64(len(r.kept) * s.pointCount())
	r.liveBytes = float64(int64(after.alloc) - int64(before.alloc))
	r.bytesPerPoint = r.liveBytes / points
	r.objectsPerPoint = (float64(after.objects) - float64(before.objects)) / points
	r.sizeRatio = float64(reported) / r.liveBytes
	return r
}

type heap struct {
	alloc   uint64
	objects uint64
}

func heapStats() heap {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	samples := []metrics.Sample{{Name: metricHeapObjects}}
	metrics.Read(samples)
	return heap{alloc: ms.HeapAlloc, objects: samples[0].Value.Uint64()}
}

// the mean wall time of a full, forced collection with the current heap live
func forcedGCMillis() float64 {
	start := time.Now()
	for range forcedGCs {
		runtime.GC()
	}
	return float64(time.Since(start).Microseconds()) / 1000 / forcedGCs
}

// keeps loadRequest's result observable, so the compiler can't drop the work
var sink atomic.Int64

type loadResult struct {
	gcCPU        float64
	assistCPU    float64
	cyclesPerSec float64
	p50, p99     float64
}

// latency buckets grow by 2^(1/8) from 1µs, so percentiles are within about 9%
const (
	latencyBuckets   = 240
	bucketsPerDouble = 8
)

type latencyHistogram [latencyBuckets]uint64

func (h *latencyHistogram) add(d time.Duration) {
	us := float64(d.Nanoseconds()) / 1e3
	i := 0
	if us > 1 {
		i = int(math.Log2(us) * bucketsPerDouble)
	}
	h[min(i, latencyBuckets-1)]++
}

func percentile(hs []latencyHistogram, p float64) float64 {
	var total uint64
	var merged latencyHistogram
	for i := range hs {
		for j, n := range hs[i] {
			merged[j] += n
			total += n
		}
	}
	want := uint64(math.Ceil(float64(total) * p))
	var seen uint64
	for j, n := range merged {
		seen += n
		if seen >= want && n > 0 {
			return math.Exp2(float64(j+1) / bucketsPerDouble)
		}
	}
	return 0
}

type loadNode struct {
	next  *loadNode
	name  string
	value []byte
}

// a request's worth of garbage that no DataSet code touches, so the load is the same in every phase:
// a linked list of small pointer-holding objects and a response-sized buffer
func loadRequest(names []string) int {
	var head *loadNode
	for i := range loadNodes {
		head = &loadNode{next: head, name: names[i%len(names)], value: make([]byte, 32)}
	}
	buf := make([]byte, 0, loadBufferBytes)
	for n := head; n != nil; n = n.next {
		buf = append(buf, n.name...)
		buf = append(buf, n.value...)
	}
	return len(buf)
}

// runLoad runs loadRequest on every CPU for d, as request handling does while a cache is full, and
// reports the GC's share of CPU and the per-request latency
func runLoad(d time.Duration) loadResult {
	names := make([]string, 16)
	for i := range names {
		names[i] = "series-" + strconv.Itoa(i)
	}
	workers := runtime.GOMAXPROCS(0)
	hists := make([]latencyHistogram, workers)
	var stop atomic.Bool
	var wg sync.WaitGroup
	samples := []metrics.Sample{
		{Name: metricGCCPU}, {Name: metricAssistCPU}, {Name: metricTotalCPU}, {Name: metricGCCycles},
	}
	metrics.Read(samples)
	gc0, assist0, total0 := samples[0].Value.Float64(), samples[1].Value.Float64(), samples[2].Value.Float64()
	cycles0 := samples[3].Value.Uint64()
	start := time.Now()
	for w := range workers {
		wg.Go(func() {
			h := &hists[w]
			for !stop.Load() {
				t0 := time.Now()
				sink.Add(int64(loadRequest(names)))
				h.add(time.Since(t0))
			}
		})
	}
	time.Sleep(d)
	stop.Store(true)
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	metrics.Read(samples)
	total := samples[2].Value.Float64() - total0
	return loadResult{
		gcCPU:        (samples[0].Value.Float64() - gc0) / total,
		assistCPU:    (samples[1].Value.Float64() - assist0) / total,
		cyclesPerSec: float64(samples[3].Value.Uint64()-cycles0) / elapsed,
		p50:          percentile(hists, 0.50),
		p99:          percentile(hists, 0.99),
	}
}
