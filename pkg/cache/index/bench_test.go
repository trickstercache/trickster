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

package index

import (
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	benchObjectSize = 1024
	benchChanges    = 1000
)

var benchObjectCounts = []int{10_000, 100_000, 1_000_000}

// a cache that keeps nothing, so that a benchmark measures the index alone
type discardClient struct{}

func (discardClient) Connect() error                            { return nil }
func (discardClient) Close() error                              { return nil }
func (discardClient) Store(string, []byte, time.Duration) error { return nil }
func (discardClient) Remove(...string) error                    { return nil }
func (discardClient) MetaStore() (blob.MetaStore, bool)         { return discardMeta{}, true }
func (discardClient) Retrieve(string) ([]byte, status.LookupStatus, error) {
	return nil, status.LookupStatusHit, nil
}

type discardMeta struct{}

type discardWriter struct{ io.Writer }

func (discardWriter) Close() error { return nil }

func (discardMeta) CreateMeta(string) (io.WriteCloser, error) { return discardWriter{io.Discard}, nil }
func (discardMeta) AppendMeta(string, []byte) error           { return nil }
func (discardMeta) OpenMeta(string) (io.ReadCloser, error)    { return nil, blob.ErrNoMeta }
func (discardMeta) RemoveMeta(...string) error                { return nil }
func (discardMeta) ListMeta() ([]string, error)               { return nil, nil }

func benchIndex(b *testing.B, n int, maxBytes int64) *IndexedClient {
	b.Helper()
	o := &options.Options{
		ReapInterval:        timeconv.Duration(time.Hour),
		FlushInterval:       timeconv.Duration(time.Hour),
		IndexExpiry:         timeconv.Duration(time.Hour),
		MaxSizeBytes:        maxBytes,
		MaxSizeBackoffBytes: benchObjectSize,
	}
	idx := NewIndexedClient("bench", "filesystem", o, discardClient{})
	b.Cleanup(func() { idx.Close() })
	benchFill(idx, n)
	idx.flushOnce()
	return idx
}

func benchFill(idx *IndexedClient, n int) {
	now := time.Now()
	for i := range n {
		at := now.Add(time.Duration(i) * time.Microsecond)
		idx.updateIndex("key-"+strconv.Itoa(i), benchObjectSize, at, at, now.Add(time.Hour+time.Duration(i%3600)*time.Second))
	}
}

func BenchmarkReapNoEviction(b *testing.B) {
	for _, n := range benchObjectCounts {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			idx := benchIndex(b, n, 1<<50)
			b.ReportAllocs()
			for b.Loop() {
				idx.reap()
			}
		})
	}
}

func BenchmarkReapEvictOne(b *testing.B) {
	for _, n := range benchObjectCounts {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			// one object over the limit, so each reap must find a little to evict
			idx := benchIndex(b, n, int64(n-1)*benchObjectSize)
			b.ReportAllocs()
			at := time.Now()
			// each pass evicts what takes the cache back under its size, and writes as much again
			for i := 0; b.Loop(); i++ {
				idx.reap()
				for j := range 2 {
					idx.updateIndex("refill-"+strconv.Itoa(i)+"-"+strconv.Itoa(j),
						benchObjectSize, at, at, at.Add(time.Hour))
				}
			}
		})
	}
}

// what it costs to journal a thousand changes and flush them, in a cache of any size
func BenchmarkFlushOnce(b *testing.B) {
	for _, n := range benchObjectCounts {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			idx := benchIndex(b, n, 1<<50)
			keys := make([]string, benchChanges)
			for i := range keys {
				keys[i] = "key-" + strconv.Itoa(i)
			}
			at := time.Now()
			b.ReportAllocs()
			for b.Loop() {
				for _, key := range keys {
					idx.updateIndex(key, benchObjectSize, at, at, at.Add(time.Hour))
				}
				idx.journal.journalBytes = 0
				idx.flushOnce()
			}
		})
	}
}

// what it costs to replace the journal with a snapshot of the whole index
func BenchmarkCompact(b *testing.B) {
	for _, n := range benchObjectCounts {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			idx := benchIndex(b, n, 1<<50)
			b.ReportAllocs()
			for b.Loop() {
				idx.journal.compact(time.Now(), idx.each)
			}
		})
	}
}

// what the index adds to each write and each read of the cache
func BenchmarkIndexedStore(b *testing.B) {
	idx := benchIndex(b, 100_000, 1<<50)
	keys := make([]string, 1024)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}
	value := make([]byte, benchObjectSize)
	b.Run("store", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			idx.Store(keys[i%len(keys)], value, time.Hour)
		}
	})
	b.Run("store/parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for i := 0; pb.Next(); i++ {
				idx.Store(keys[i%len(keys)], value, time.Hour)
			}
		})
	})
	b.Run("retrieve", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			idx.Retrieve(keys[i%len(keys)])
		}
	})
	b.Run("retrieve/parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for i := 0; pb.Next(); i++ {
				idx.Retrieve(keys[i%len(keys)])
			}
		})
	})
}
