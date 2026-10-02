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

package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	bbo "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	flo "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	benchKeyCount    = 64
	benchParallelism = 64
	benchTTL         = time.Hour
)

var benchSizes = []struct {
	name string
	size int
}{
	{"1KiB", 1 << 10},
	{"64KiB", 64 << 10},
	{"4MiB", 4 << 20},
}

func benchDiskCache(b *testing.B, provider string) cache.Cache {
	b.Helper()
	dir := b.TempDir()
	cfg := &co.Options{
		Name:       "bench",
		Provider:   provider,
		Filesystem: &flo.Options{CachePath: dir + "/fs"},
		BBolt:      &bbo.Options{Filename: dir + "/bench.db", Bucket: "bench"},
		Index: &io.Options{
			ReapInterval:  timeconv.Duration(time.Hour),
			FlushInterval: timeconv.Duration(time.Hour),
			IndexExpiry:   timeconv.Duration(time.Hour),
			MaxSizeBytes:  1 << 40,
		},
	}
	c := NewCache("bench", cfg)
	b.Cleanup(func() { c.Close() })
	return c
}

func benchKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		sum := sha256.Sum256([]byte(strconv.Itoa(i)))
		keys[i] = hex.EncodeToString(sum[:])
	}
	return keys
}

func benchPayload(size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(i)
	}
	return p
}

// serially, then across benchParallelism goroutines per CPU
func runBench(b *testing.B, size int, fn func(i int) error) {
	b.Run("serial", func(b *testing.B) {
		b.SetBytes(int64(size))
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			if err := fn(i); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.SetBytes(int64(size))
		b.ReportAllocs()
		b.SetParallelism(benchParallelism)
		var n atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := fn(int(n.Add(1))); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func BenchmarkDiskCache(b *testing.B) {
	keys := benchKeys(benchKeyCount)
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		for _, bs := range benchSizes {
			payload := benchPayload(bs.size)
			prefix := provider + "/" + bs.name + "/"
			b.Run(prefix+"store", func(b *testing.B) {
				c := benchDiskCache(b, provider)
				runBench(b, bs.size, func(i int) error {
					return c.Store(keys[i%benchKeyCount], payload, benchTTL)
				})
			})
			b.Run(prefix+"hit", func(b *testing.B) {
				c := benchDiskCache(b, provider)
				for _, k := range keys {
					if err := c.Store(k, payload, benchTTL); err != nil {
						b.Fatal(err)
					}
				}
				runBench(b, bs.size, func(i int) error {
					_, _, err := c.Retrieve(keys[i%benchKeyCount])
					return err
				})
			})
			b.Run(prefix+"miss", func(b *testing.B) {
				c := benchDiskCache(b, provider)
				runBench(b, 0, func(i int) error {
					c.Retrieve(keys[i%benchKeyCount])
					return nil
				})
			})
		}
	}
}
