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

package redis

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	vo "github.com/trickstercache/trickster/v2/pkg/cache/valkey/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/valkey-io/valkey-go"
)

// The live benchmarks compare cache clients against running servers. They run only when
// TRICKSTER_REDIS_BENCH=1, as with `make benchmark-redis` against the developer environment.
const (
	envBench             = "TRICKSTER_REDIS_BENCH"
	envBenchRedisAddr    = "TRICKSTER_REDIS_ADDR"
	envBenchValkeyAddr   = "TRICKSTER_VALKEY_ADDR"
	envBenchClusterAddr  = "TRICKSTER_VALKEY_CLUSTER_ADDR"
	envBenchDelay        = "TRICKSTER_REDIS_BENCH_DELAY"
	defaultBenchRedis    = "127.0.0.1:6379"
	defaultBenchValkey   = "127.0.0.1:6380"
	defaultBenchCluster  = "127.0.0.1:7001"
	defaultBenchDelay    = 500 * time.Microsecond
	benchTTL             = 10 * time.Minute
	benchSmallValue      = 4 << 10
	benchLargeValue      = 4 << 20
	benchHOLWriters      = 4
	benchMixedKeys       = 10000
	benchClientCacheKeys = 1000
	benchPurgeKeys       = 10
	benchSampleCap       = 1 << 16
)

var benchSizes = []int{256, 4 << 10, 64 << 10, 512 << 10, 4 << 20}

// benchClient is what the benchmarks need of a cache client
type benchClient interface {
	Store(key string, data []byte, ttl time.Duration) error
	Retrieve(key string) ([]byte, status.LookupStatus, error)
	Remove(keys ...string) error
	Close() error
}

type benchConfig struct {
	name string
	// tuning marks a valkey setting that only matters to pipelining, run on the Valkey servers only
	tuning bool
	open   func(b *testing.B, ct, addr string) benchClient
}

type benchServer struct {
	name, ct, addr string
	valkey         bool
}

func valkeyBenchConfig(name string, tuning bool, mut func(*vo.Options)) benchConfig {
	return benchConfig{name: name, tuning: tuning, open: func(b *testing.B, ct, addr string) benchClient {
		v := vo.New()
		v.Options = *benchRedisOptions(ct, addr)
		v.AutoPipelining = true
		if mut != nil {
			mut(v)
		}
		return openCacheClient(b, &co.Options{Provider: cp.Valkey, ProviderID: cp.ValkeyID, Valkey: v})
	}}
}

func multiplex(n int) func(*vo.Options) {
	return func(o *vo.Options) { o.PipelineMultiplex = &n }
}

// benchConfigs are the clients each workload compares
var benchConfigs = []benchConfig{
	{name: "classic", open: func(b *testing.B, ct, addr string) benchClient {
		return openCacheClient(b, &co.Options{Provider: cp.Redis, ProviderID: cp.RedisID, Redis: benchRedisOptions(ct, addr)})
	}},
	valkeyBenchConfig("pipelined", false, nil),
	valkeyBenchConfig("pipelined-mx0", true, multiplex(0)),
	valkeyBenchConfig("pipelined-mx1", true, multiplex(1)),
	valkeyBenchConfig("pipelined-mx3", true, multiplex(3)),
	valkeyBenchConfig("pipelined-mx5", true, multiplex(5)),
	valkeyBenchConfig("pipelined-mx8", true, multiplex(8)),
	{name: "pipelined-dedicated", tuning: true, open: func(b *testing.B, ct, addr string) benchClient {
		v := vo.New()
		v.Options = *benchRedisOptions(ct, addr)
		v.AutoPipelining = true
		return dedicatedLargeStores{openCacheClient(b, &co.Options{Provider: cp.Valkey, ProviderID: cp.ValkeyID,
			Valkey: v}).(*CacheClient)}
	}},
	valkeyBenchConfig("pipelined-flush20us", true, func(o *vo.Options) {
		o.MaxFlushDelay = timeconv.Duration(20 * time.Microsecond)
	}),
}

func benchRedisOptions(ct, addr string) *ro.Options {
	o := &ro.Options{ClientType: ct, Endpoint: addr}
	if ct == ro.ClientTypeCluster {
		o.Endpoint, o.Endpoints = "", []string{addr}
	}
	return o
}

func openCacheClient(b *testing.B, o *co.Options) benchClient {
	b.Helper()
	c := New(context.Background(), "bench", o)
	if err := c.Connect(); err != nil {
		b.Fatal(err)
	}
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func benchServers(b *testing.B) []benchServer {
	b.Helper()
	if os.Getenv(envBench) != "1" {
		b.Skip("set " + envBench + "=1 to run the live redis and valkey benchmarks")
	}
	candidates := []benchServer{
		{name: "redis", ct: ro.ClientTypeStandard, addr: envOr(envBenchRedisAddr, defaultBenchRedis)},
		{name: "valkey", ct: ro.ClientTypeStandard, addr: envOr(envBenchValkeyAddr, defaultBenchValkey), valkey: true},
		{name: "valkey-cluster", ct: ro.ClientTypeCluster, addr: envOr(envBenchClusterAddr, defaultBenchCluster), valkey: true},
	}
	var out []benchServer
	for _, s := range candidates {
		if c, err := net.DialTimeout("tcp", s.addr, time.Second); err == nil {
			c.Close()
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		b.Skip("no redis or valkey server is reachable")
	}
	return out
}

func benchDelay(b *testing.B) time.Duration {
	b.Helper()
	d, err := time.ParseDuration(envOr(envBenchDelay, defaultBenchDelay.String()))
	if err != nil {
		b.Fatal(err)
	}
	return d
}

func benchValue(size int) []byte {
	r := weaktest.NewRand(uint64(size), 1)
	v := make([]byte, size)
	for i := range v {
		v[i] = byte(r.Uint32())
	}
	return v
}

// runBench opens a client per config and runs fn on it, over each reachable server and, for
// single servers, over a proxy adding a fixed delay each way. Results are named
// server=…/net=…/<workload>/client=… so benchstat can compare clients with -col /client.
func runBench(b *testing.B, workload string, includeTuning bool,
	fn func(b *testing.B, s benchServer, c benchClient),
) {
	delay := benchDelay(b)
	for _, s := range benchServers(b) {
		nets := []string{"direct"}
		if s.ct == ro.ClientTypeStandard && delay > 0 {
			nets = append(nets, "delay"+delay.String())
		}
		for _, network := range nets {
			addr := s.addr
			if network != "direct" {
				addr = startDelayProxy(b, s.addr, delay)
			}
			for _, cfg := range benchConfigs {
				if cfg.tuning && (!includeTuning || !s.valkey) {
					continue
				}
				b.Run(fmt.Sprintf("server=%s/net=%s/%s/client=%s", s.name, network, workload, cfg.name),
					func(b *testing.B) {
						c := cfg.open(b, s.ct, addr)
						defer c.Close()
						fn(b, s, c)
					})
			}
		}
	}
}

func mustStore(b *testing.B, c benchClient, key string, v []byte) {
	b.Helper()
	if err := c.Store(key, v, benchTTL); err != nil {
		b.Fatal(err)
	}
}

func mustHit(b *testing.B, c benchClient, key string) {
	if _, ls, err := c.Retrieve(key); err != nil || ls != status.LookupStatusHit {
		b.Fatalf("retrieve %s: %s %v", key, ls, err)
	}
}

// BenchmarkLiveGetHit is one goroutine reading values of each size
func BenchmarkLiveGetHit(b *testing.B) {
	runBench(b, "get-hit", false, func(b *testing.B, _ benchServer, c benchClient) {
		for _, size := range benchSizes {
			key := "bench:hit:" + strconv.Itoa(size)
			mustStore(b, c, key, benchValue(size))
			b.Run("size="+strconv.Itoa(size), func(b *testing.B) {
				b.SetBytes(int64(size))
				for b.Loop() {
					mustHit(b, c, key)
				}
			})
		}
	})
}

// BenchmarkLiveGetParallel is many goroutines reading small values, where pipelining helps
func BenchmarkLiveGetParallel(b *testing.B) {
	runBench(b, "get-parallel", true, func(b *testing.B, _ benchServer, c benchClient) {
		const keys = 64
		v := benchValue(benchSmallValue)
		for i := range keys {
			mustStore(b, c, "bench:par:"+strconv.Itoa(i), v)
		}
		for _, p := range []int{1, 8, 64, 512} {
			b.Run("p="+strconv.Itoa(p), func(b *testing.B) {
				b.SetParallelism(p)
				var next atomic.Int64
				b.RunParallel(func(pb *testing.PB) {
					key := "bench:par:" + strconv.Itoa(int(next.Add(1)%keys))
					for pb.Next() {
						mustHit(b, c, key)
					}
				})
			})
		}
	})
}

// skewedIndex returns an index in [0, n) that favors low indexes, as a few cache objects are hot
func skewedIndex(r *weaktest.Rand, n int) int {
	u := r.Float64()
	return int(u * u * u * float64(n))
}

// BenchmarkLiveMixed is a 90/10 read/write mix over skewed keys from 64 goroutines
func BenchmarkLiveMixed(b *testing.B) {
	runBench(b, "mixed", true, func(b *testing.B, _ benchServer, c benchClient) {
		v := benchValue(benchSmallValue)
		for i := range benchMixedKeys {
			mustStore(b, c, "bench:mix:"+strconv.Itoa(i), v)
		}
		b.ResetTimer()
		b.SetParallelism(64)
		var seed atomic.Uint64
		b.RunParallel(func(pb *testing.PB) {
			r := weaktest.NewRand(seed.Add(1), 2)
			for pb.Next() {
				key := "bench:mix:" + strconv.Itoa(skewedIndex(r, benchMixedKeys))
				if r.IntN(10) == 0 {
					mustStore(b, c, key, v)
					continue
				}
				mustHit(b, c, key)
			}
		})
	})
}

// BenchmarkLiveHeadOfLine reads small values while other goroutines move large ones, and
// reports the small reads' latency percentiles, which pipelining behind large replies can raise
func BenchmarkLiveHeadOfLine(b *testing.B) {
	runBench(b, "head-of-line", true, func(b *testing.B, _ benchServer, c benchClient) {
		small, large := benchValue(benchSmallValue), benchValue(benchLargeValue)
		mustStore(b, c, "bench:hol:small", small)
		for i := range benchHOLWriters {
			mustStore(b, c, "bench:hol:large:"+strconv.Itoa(i), large)
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for i := range benchHOLWriters {
			wg.Go(func() {
				key := "bench:hol:large:" + strconv.Itoa(i)
				for n := 0; ; n++ {
					select {
					case <-stop:
						return
					default:
					}
					if n%2 == 0 {
						_ = c.Store(key, large, benchTTL)
					} else {
						_, _, _ = c.Retrieve(key)
					}
				}
			})
		}
		var mu sync.Mutex
		var samples []time.Duration
		b.ResetTimer()
		b.SetParallelism(8)
		b.RunParallel(func(pb *testing.PB) {
			local := make([]time.Duration, 0, benchSampleCap)
			for pb.Next() {
				start := time.Now()
				mustHit(b, c, "bench:hol:small")
				if len(local) < benchSampleCap {
					local = append(local, time.Since(start))
				}
			}
			mu.Lock()
			samples = append(samples, local...)
			mu.Unlock()
		})
		close(stop)
		wg.Wait()
		reportPercentiles(b, samples)
	})
}

func reportPercentiles(b *testing.B, samples []time.Duration) {
	if len(samples) == 0 {
		return
	}
	slices.Sort(samples)
	for _, p := range []struct {
		unit string
		q    float64
	}{{"p50-ns", 0.50}, {"p99-ns", 0.99}, {"p999-ns", 0.999}} {
		b.ReportMetric(float64(samples[int(p.q*float64(len(samples)-1))].Nanoseconds()), p.unit)
	}
}

// BenchmarkLiveGetMiss is one goroutine reading a key that isn't there, as a cold cache does
func BenchmarkLiveGetMiss(b *testing.B) {
	runBench(b, "get-miss", false, func(b *testing.B, _ benchServer, c benchClient) {
		for b.Loop() {
			if _, ls, _ := c.Retrieve("bench:miss"); ls != status.LookupStatusKeyMiss {
				b.Fatalf("expected a miss, got %s", ls)
			}
		}
	})
}

// BenchmarkLivePurge removes several keys at once, as a path purge does
func BenchmarkLivePurge(b *testing.B) {
	runBench(b, "purge", false, func(b *testing.B, _ benchServer, c benchClient) {
		v := benchValue(benchSmallValue)
		keys := make([]string, benchPurgeKeys)
		for i := range keys {
			keys[i] = "bench:purge:" + strconv.Itoa(i)
		}
		for b.Loop() {
			b.StopTimer()
			for _, k := range keys {
				mustStore(b, c, k, v)
			}
			b.StartTimer()
			if err := c.Remove(keys...); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkLiveClientCache compares reads with and without the client-side cache on Valkey: a
// skewed read-only load, which it mostly serves locally, and misses, which cost it more commands
func BenchmarkLiveClientCache(b *testing.B) {
	v := benchValue(benchSmallValue)
	for _, s := range benchServers(b) {
		if !s.valkey {
			continue
		}
		for _, csc := range []bool{false, true} {
			cfg := valkeyBenchConfig("", false, func(o *vo.Options) { o.ClientSideCache.Enabled = csc })
			name := fmt.Sprintf("server=%s/client-cache=%v", s.name, csc)
			b.Run(name+"/skewed-reads", func(b *testing.B) {
				c := cfg.open(b, s.ct, s.addr)
				defer c.Close()
				for i := range benchClientCacheKeys {
					mustStore(b, c, "bench:csc:"+strconv.Itoa(i), v)
				}
				b.ResetTimer()
				b.SetParallelism(8)
				var seed atomic.Uint64
				b.RunParallel(func(pb *testing.PB) {
					r := weaktest.NewRand(seed.Add(1), 3)
					for pb.Next() {
						mustHit(b, c, "bench:csc:"+strconv.Itoa(skewedIndex(r, benchClientCacheKeys)))
					}
				})
			})
			b.Run(name+"/misses", func(b *testing.B) {
				c := cfg.open(b, s.ct, s.addr)
				defer c.Close()
				n := 0
				for b.Loop() {
					n++
					if _, ls, _ := c.Retrieve("bench:csc:miss:" + strconv.Itoa(n)); ls != status.LookupStatusKeyMiss {
						b.Fatalf("expected a miss, got %s", ls)
					}
				}
			})
		}
	}
}

// dedicatedLargeStores writes large values on a pooled connection of their own, so that their
// requests don't hold up the commands pipelined behind them
type dedicatedLargeStores struct {
	*CacheClient
}

const benchDedicatedThreshold = 256 << 10

func (d dedicatedLargeStores) Store(key string, data []byte, ttl time.Duration) error {
	if len(data) < benchDedicatedThreshold {
		return d.CacheClient.Store(key, data, ttl)
	}
	cl, err := d.Holder().Client()
	if err != nil {
		return err
	}
	return cl.Dedicated(func(dc valkey.DedicatedClient) error {
		return dc.Do(context.Background(), dc.B().Set().Key(key).Value(valkey.BinaryString(data)).
			PxMilliseconds(ttl.Milliseconds()).Build()).Error()
	})
}

// startDelayProxy forwards connections to target, holding each chunk for delay in each
// direction without limiting throughput, as a network hop between availability zones would
func startDelayProxy(b *testing.B, target string, delay time.Duration) string {
	b.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				in.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, in, out)
			mu.Unlock()
			go delayCopy(out, in, delay)
			go delayCopy(in, out, delay)
		}
	}()
	b.Cleanup(func() {
		l.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	return l.Addr().String()
}

type delayedChunk struct {
	data []byte
	at   time.Time
}

func delayCopy(dst, src net.Conn, delay time.Duration) {
	q := make(chan delayedChunk, 4096)
	go func() {
		defer close(q)
		buf := make([]byte, 64<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				q <- delayedChunk{data: append([]byte(nil), buf[:n]...), at: time.Now().Add(delay)}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range q {
		if wait := time.Until(c.at); wait > 0 {
			time.Sleep(wait)
		}
		if _, err := dst.Write(c.data); err != nil {
			break
		}
	}
	_ = dst.Close()
	_, _ = io.Copy(io.Discard, src)
}
