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
	"context"
	"errors"
	"hash/maphash"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	gm "github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// keyLockCount is how many locks keys are spread over. It must be a power of two.
	keyLockCount = 4096
	keyLockMask  = keyLockCount - 1

	workerFlusher = "flusher"
	workerReaper  = "reaper"
	workerScanner = "scanner"

	eventEviction    = "eviction"
	eventIndex       = "index"
	reasonCompaction = "compaction"
	reasonSweep      = "sweep"
	reasonAdopted    = "adopted"
	reasonDropped    = "dropped"
	reasonTTL        = "ttl"
	reasonBytes      = "size_bytes"
	reasonObjects    = "size_objects"
	reasonFreeSpace  = "free_space"
)

// IndexedClient implements the cache.Client and cache.SplitClient interfaces
var (
	_ cache.Client       = &IndexedClient{}
	_ cache.SplitClient  = &IndexedClient{}
	_ cache.StreamClient = &IndexedClient{}
)

// ErrSplitUnsupported is returned by StoreSplit, RetrieveSplit and OpenSplit when the
// underlying cache keeps objects whole, or cannot leave them open
var ErrSplitUnsupported = errors.New("cache does not keep objects in sections")

var ErrIndexInvalidCacheKey = errors.New("cannot store index")

// IndexedClientOptions modify an IndexedClient's behavior.
type IndexedClientOptions struct {
	NeedsFlushInterval bool
	NeedsReapInterval  bool
	// MinFreeBytes is the space the index keeps free on the cache's medium, when the
	// cache can report it, by evicting as it would for a cache that is over its size
	MinFreeBytes int64
}

// a cache that can hold the index's own files
type metaStorer interface {
	MetaStore() (blob.MetaStore, bool)
}

// a cache that can report the space left on its medium
type freeSpacer interface {
	FreeBytes() (int64, bool)
}

// IndexedClient tracks what a cache that keeps all it is given holds, like filesystem or
// bbolt, and enforces retention for it. Redis manages its own, and has no index.
type IndexedClient struct {
	// Client is the underlying cache client used by the Index
	Client cache.Client

	// objects holds each *Object in the cache by its key, and is read without locking
	objects sync.Map
	shards  shards
	// cacheSize is the size of the cache in bytes, and objectCount the count of its objects
	cacheSize   atomic.Int64
	objectCount atomic.Int64

	name          string
	cacheProvider string
	options       atomic.Pointer[options.Options]
	ico           IndexedClientOptions
	objectsGauge  prometheus.Gauge
	bytesGauge    prometheus.Gauge

	// journal persists the index, and is nil when the index is not persisted
	journal *journal
	// scanner lists what the cache really holds, and is nil when the cache cannot
	scanner cache.Scanner
	// split keeps objects in two sections, and is nil when the cache cannot
	split cache.SplitClient
	// stream reads objects in parts, and is nil when the cache cannot
	stream cache.StreamClient
	// keyLocks serialize the storing and removing of a key, each of which changes the
	// cache and then the index, so that neither sees the other half done
	keyLocks [keyLockCount]sync.Mutex
	free     freeSpacer
	// lastFlush is when the index was last persisted, in Unix nanoseconds
	lastFlush atomic.Int64
	// pressure is set by a write that takes the cache over its size, to hasten the reaper
	pressure atomic.Bool
	// sweeps numbers the sweeps of the cache, and sweepDue asks for one at the start
	sweeps   atomic.Uint64
	sweepDue bool

	isClosing     atomic.Bool
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	flusherExited atomic.Bool
	reaperExited  atomic.Bool
	scannerExited atomic.Bool

	// used only in tests: fields to interact with client goroutines
	forceFlush chan bool
	hasFlushed chan bool
	forceReap  chan bool
	hasReaped  chan bool
	forceSweep chan bool
	hasSwept   chan bool
}

func NewIndexedClient(
	cacheName, cacheProvider string,
	o *options.Options,
	client cache.Client,
	opts ...func(*IndexedClientOptions),
) *IndexedClient {
	ctx, cancel := context.WithCancel(context.Background())
	idx := &IndexedClient{
		Client:        client,
		name:          cacheName,
		cacheProvider: cacheProvider,
		cancel:        cancel,
		objectsGauge:  gm.CacheObjects.WithLabelValues(cacheName, cacheProvider),
		bytesGauge:    gm.CacheBytes.WithLabelValues(cacheName, cacheProvider),
		forceFlush:    make(chan bool),
		hasFlushed:    make(chan bool, 1),
		forceReap:     make(chan bool),
		hasReaped:     make(chan bool, 1),
		forceSweep:    make(chan bool),
		hasSwept:      make(chan bool, 1),
	}
	idx.options.Store(o)
	for _, opt := range opts {
		opt(&idx.ico)
	}
	idx.scanner, _ = client.(cache.Scanner)
	if sc, ok := client.(cache.SplitClient); ok && sc.SupportsSplit() {
		idx.split = sc
	}
	if sc, ok := client.(cache.StreamClient); ok && sc.SupportsStream() {
		idx.stream = sc
	}
	idx.free, _ = client.(freeSpacer)

	if idx.ico.NeedsFlushInterval || o.FlushInterval > 0 {
		idx.load(client, time.Duration(o.IndexExpiry))
		if o.FlushInterval > 0 && idx.journal != nil {
			idx.wg.Add(1)
			go idx.flusher(ctx)
		} else if idx.ico.NeedsFlushInterval {
			logger.Warn("cache index flusher was not started, recommended for provider",
				logging.Pairs{keys.CacheName: idx.name, keys.CacheProvider: idx.cacheProvider, "flushInterval": o.FlushInterval})
		}
	}

	if o.ReapInterval > 0 {
		idx.wg.Add(1)
		go idx.reaper(ctx)
	} else if idx.ico.NeedsReapInterval {
		logger.Warn("cache reaper was not started, recommended for provider",
			logging.Pairs{keys.CacheName: idx.name, keys.CacheProvider: idx.cacheProvider, "reapInterval": o.ReapInterval})
	}

	if idx.scanner != nil && (idx.sweepDue || o.ScanInterval > 0) {
		idx.wg.Add(1)
		go idx.scannerWorker(ctx)
	}

	gm.CacheMaxObjects.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeObjects))
	gm.CacheMaxBytes.WithLabelValues(cacheName, cacheProvider).Set(float64(o.MaxSizeBytes))
	return idx
}

// whatever is missing from what was persisted is left for a sweep of the cache to find
func (idx *IndexedClient) load(client cache.Client, expiry time.Duration) {
	ms, ok := client.(metaStorer)
	if !ok {
		return
	}
	store, ok := ms.MetaStore()
	if !ok {
		return
	}
	idx.journal = &journal{store: store}
	nowNano := time.Now().UnixNano()
	l, err := idx.journal.load(func(r *record) {
		if r.op == opAdd && (r.expiration == 0 || r.expiration > nowNano) {
			idx.restore(r)
			return
		}
		// removed, or expired since: neither is journaled again, as the journal has it already
		idx.unlist(r.key)
	})
	stale := expiry > 0 && l.lastFlush > 0 && nowNano-l.lastFlush > int64(expiry)
	if err != nil || stale {
		if err != nil {
			logger.Warn("cache index was not loaded",
				logging.Pairs{keys.CacheName: idx.name, keys.Error: err.Error()})
		}
		idx.Clear()
		idx.journal.markLossy()
		l.whole = false
	}
	idx.lastFlush.Store(l.lastFlush)
	// an index that is whole and was closed in good order lists all the cache holds
	idx.sweepDue = !l.whole || !l.clean
}

var keyLockSeed = maphash.MakeSeed()

func (idx *IndexedClient) keyLock(cacheKey string) *sync.Mutex {
	return &idx.keyLocks[maphash.String(keyLockSeed, cacheKey)&keyLockMask]
}

// each lock once, in one order, so that batches never deadlock one another
func (idx *IndexedClient) lockKeys(cacheKeys []string) func() {
	var few [evictionBatch]int
	stripes := few[:0]
	for _, key := range cacheKeys {
		stripes = append(stripes, int(maphash.String(keyLockSeed, key)&keyLockMask))
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, i := range stripes {
		idx.keyLocks[i].Lock()
	}
	return func() {
		for _, i := range stripes {
			idx.keyLocks[i].Unlock()
		}
	}
}

// Size returns the size in bytes of the objects in the cache
func (idx *IndexedClient) Size() int64 {
	return idx.cacheSize.Load()
}

// Count returns the count of objects in the cache
func (idx *IndexedClient) Count() int64 {
	return idx.objectCount.Load()
}

// Object returns what the index knows of the object stored under cacheKey
func (idx *IndexedClient) Object(cacheKey string) (*Object, bool) {
	v, ok := idx.objects.Load(cacheKey)
	if !ok {
		return nil, false
	}
	return v.(*Object), true
}

// Keys returns the cache keys of the objects in the cache, in no particular order
func (idx *IndexedClient) Keys() []string {
	keys := make([]string, 0, max(idx.objectCount.Load(), 0))
	idx.objects.Range(func(k, _ any) bool {
		keys = append(keys, k.(string))
		return true
	})
	return keys
}

// a shard at a time, under that shard's lock alone
func (idx *IndexedClient) each(yield func(o *Object) bool) {
	var scratch []*Object
	for i := range idx.shards {
		scratch = idx.shards[i].appendAll(scratch[:0])
		for _, o := range scratch {
			if !yield(o) {
				return
			}
		}
	}
}

// Clear the index from its currently tracked cache objects
func (idx *IndexedClient) Clear() {
	idx.objects.Clear()
	for i := range idx.shards {
		idx.shards[i].clear()
	}
	idx.cacheSize.Store(0)
	idx.objectCount.Store(0)
	idx.observeSize(0, 0)
}

// UpdateOptions updates the existing IndexedClient with a new Options reference
func (idx *IndexedClient) UpdateOptions(o *options.Options) {
	idx.options.Store(o)
}

// No-op -- implements the cache.Client interface
func (idx *IndexedClient) Connect() error {
	return nil
}

func (idx *IndexedClient) observeSize(size, count int64) {
	idx.objectsGauge.Set(float64(count))
	idx.bytesGauge.Set(float64(size))
}

// notes a cache that has grown past its size, which hastens the reaper
func (idx *IndexedClient) count(size, objects int64) {
	total := idx.cacheSize.Add(size)
	n := idx.objectCount.Add(objects)
	idx.observeSize(total, n)
	if size <= 0 && objects <= 0 {
		return
	}
	o := idx.options.Load()
	if ((o.MaxSizeBytes > 0 && total > o.MaxSizeBytes) || (o.MaxSizeObjects > 0 && n > o.MaxSizeObjects)) &&
		!idx.pressure.Load() {
		idx.pressure.Store(true)
	}
}

// returns the object's entry, and whether the entry was a transient one before
func (idx *IndexedClient) put(cacheKey string, size, lastAccess, lastWrite, expiration int64, transient bool) (*Object, bool) {
	s := idx.shards.of(cacheKey)
	for {
		v, ok := idx.objects.Load(cacheKey)
		if !ok {
			o := &Object{Key: cacheKey, slot: noSlot}
			o.transient.Store(transient)
			o.size.Store(size)
			o.lastAccess.Store(lastAccess)
			o.lastWrite.Store(lastWrite)
			o.expiration.Store(expiration)
			if v, ok = idx.objects.LoadOrStore(cacheKey, o); !ok {
				if counted, inserted := s.insert(o, lastWrite); inserted {
					idx.count(counted, 1)
				}
				return o, true
			}
		}
		o := v.(*Object)
		if delta, ok := s.update(o, size, expiration, lastWrite); ok {
			o.lastAccess.Store(lastAccess)
			idx.count(delta, 0)
			return o, o.transient.Swap(transient)
		}
		// the object left the index as it was being written, and is listed anew
	}
}

func (idx *IndexedClient) updateIndex(cacheKey string, size int64, la, lw, e time.Time) {
	transient := !e.IsZero() && e.Sub(lw) < transientTTL
	o, was := idx.put(cacheKey, size, la.UnixNano(), lw.UnixNano(), unixNano(e), transient)
	switch {
	case !transient:
		idx.journal.add(o)
	case !was:
		// what the journal holds of the object is no longer so, and is not to be restored
		idx.journal.remove(cacheKey)
	}
}

// an object read back from the journal is not journaled again
func (idx *IndexedClient) restore(r *record) {
	idx.put(r.key, r.size, r.lastAccess, r.lastWrite, r.expiration, false)
}

func (idx *IndexedClient) Store(cacheKey string, byteData []byte, ttl time.Duration) error {
	if cacheKey == IndexKey {
		return ErrIndexInvalidCacheKey
	}
	mu := idx.keyLock(cacheKey)
	mu.Lock()
	defer mu.Unlock()
	if err := idx.Client.Store(cacheKey, byteData, ttl); err != nil {
		return err
	}
	now := time.Now()
	var expiry time.Time
	if ttl > 0 {
		expiry = now.Add(ttl)
	}
	idx.updateIndex(cacheKey, int64(len(byteData)), now, now, expiry)
	return nil
}

// SupportsSplit reports whether the underlying cache keeps objects in two sections
func (idx *IndexedClient) SupportsSplit() bool {
	return idx.split != nil
}

// StoreSplit implements the cache.SplitClient interface, and lists the object as Store does
func (idx *IndexedClient) StoreSplit(cacheKey string, meta, body []byte, ttl time.Duration) error {
	if cacheKey == IndexKey {
		return ErrIndexInvalidCacheKey
	}
	if idx.split == nil {
		return ErrSplitUnsupported
	}
	mu := idx.keyLock(cacheKey)
	mu.Lock()
	defer mu.Unlock()
	if err := idx.split.StoreSplit(cacheKey, meta, body, ttl); err != nil {
		return err
	}
	now := time.Now()
	var expiry time.Time
	if ttl > 0 {
		expiry = now.Add(ttl)
	}
	idx.updateIndex(cacheKey, int64(len(meta)+len(body)), now, now, expiry)
	return nil
}

// RetrieveSplit implements the cache.SplitClient interface, and notes the access as Retrieve does
func (idx *IndexedClient) RetrieveSplit(cacheKey string) ([]byte, []byte, status.LookupStatus, error) {
	if cacheKey == IndexKey {
		return nil, nil, status.LookupStatusError, ErrIndexInvalidCacheKey
	}
	if idx.split == nil {
		return nil, nil, status.LookupStatusError, ErrSplitUnsupported
	}
	now := time.Now()
	meta, body, s, err := idx.split.RetrieveSplit(cacheKey)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			idx.forgetUnlessWrittenSince(cacheKey, now)
		}
		return nil, nil, s, err
	}
	if s != status.LookupStatusHit {
		return nil, nil, s, err
	}
	idx.updateAccessTime(cacheKey, now)
	return meta, body, s, nil
}

// SupportsStream reports whether the underlying cache reads objects in parts
func (idx *IndexedClient) SupportsStream() bool {
	return idx.stream != nil
}

// OpenSplit implements the cache.StreamClient interface, and notes the access as Retrieve does
func (idx *IndexedClient) OpenSplit(cacheKey string) ([]byte, cache.Body, status.LookupStatus, error) {
	if cacheKey == IndexKey {
		return nil, nil, status.LookupStatusError, ErrIndexInvalidCacheKey
	}
	if idx.stream == nil {
		return nil, nil, status.LookupStatusError, ErrSplitUnsupported
	}
	now := time.Now()
	meta, body, s, err := idx.stream.OpenSplit(cacheKey)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			idx.forgetUnlessWrittenSince(cacheKey, now)
		}
		return nil, nil, s, err
	}
	idx.updateAccessTime(cacheKey, now)
	return meta, body, s, nil
}

// the access time is the one time.Now of a retrieval, taken before the cache is read
func (idx *IndexedClient) updateAccessTime(cacheKey string, at time.Time) {
	if v, ok := idx.objects.Load(cacheKey); ok {
		v.(*Object).lastAccess.Store(at.UnixNano())
	}
}

// Retrieve implements the cache.Client interface, looking up the object and updating the index last access time
func (idx *IndexedClient) Retrieve(cacheKey string) ([]byte, status.LookupStatus, error) {
	if cacheKey == IndexKey {
		return nil, status.LookupStatusError, ErrIndexInvalidCacheKey
	}
	now := time.Now()
	data, s, err := idx.Client.Retrieve(cacheKey)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			// the cache no longer holds what the index lists, so the index lets it go
			idx.forgetUnlessWrittenSince(cacheKey, now)
		}
		return nil, s, err
	}
	if s != status.LookupStatusHit {
		return nil, s, err
	}
	idx.updateAccessTime(cacheKey, now)
	return data, s, nil
}

// drops the object from the index without journaling it, and returns the bytes it had
// accounted for
func (idx *IndexedClient) unlist(cacheKey string) (*Object, int64) {
	v, ok := idx.objects.LoadAndDelete(cacheKey)
	if !ok {
		return nil, 0
	}
	o := v.(*Object)
	size := idx.shards.of(cacheKey).remove(o)
	idx.count(-size, -1)
	return o, size
}

// drops the object from the index alone; the cache is left to the caller
func (idx *IndexedClient) forget(cacheKey string) int64 {
	o, size := idx.unlist(cacheKey)
	if o != nil && !o.transient.Load() {
		idx.journal.remove(cacheKey)
	}
	return size
}

// an object written since it was chosen for removal is no longer the object that was to go,
// and stays
func (idx *IndexedClient) forgetUnlessWrittenSince(cacheKey string, since time.Time) (int64, bool) {
	v, ok := idx.objects.Load(cacheKey)
	if !ok {
		return 0, false
	}
	o := v.(*Object)
	s := idx.shards.of(cacheKey)
	s.mu.Lock()
	if o.lastWrite.Load() >= since.UnixNano() || !idx.objects.CompareAndDelete(cacheKey, o) {
		s.mu.Unlock()
		return 0, false
	}
	size := s.removeLocked(o)
	s.mu.Unlock()
	idx.count(-size, -1)
	if !o.transient.Load() {
		idx.journal.remove(cacheKey)
	}
	return size, true
}

// Remove implements the cache.Client interface and removes the object from the cache and index
func (idx *IndexedClient) Remove(cacheKeys ...string) error {
	released, err := idx.remove(cacheKeys)
	// the cache manager records the delete operation; only the index knows the freed byte count
	metrics.ObserveCacheDelBytes(idx.name, idx.cacheProvider, float64(released))
	return err
}

func (idx *IndexedClient) remove(cacheKeys []string) (int64, error) {
	unlock := idx.lockKeys(cacheKeys)
	defer unlock()
	var released int64
	for _, key := range cacheKeys {
		released += idx.forget(key)
	}
	return released, idx.Client.Remove(cacheKeys...)
}

// removals chosen before since, from the index and the cache alike; any written again in the
// meantime are left alone
func (idx *IndexedClient) evict(reason string, since time.Time, removals []string) {
	metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, eventEviction, reason)
	// reaper removals bypass the cache manager, so the deletion is recorded here
	start := time.Now()
	unlock := idx.lockKeys(removals)
	var released int64
	// under the locks, what the index still lists as older than since is what the cache holds
	gone := removals[:0]
	for _, key := range removals {
		if size, dropped := idx.forgetUnlessWrittenSince(key, since); dropped {
			released += size
			gone = append(gone, key)
		} else if v, ok := idx.objects.Load(key); ok {
			// spared, and so to be chosen again another time
			o := v.(*Object)
			o.evicting.Store(false)
			if reason == reasonTTL {
				idx.shards.of(key).reschedule(o, start.UnixNano())
			}
		}
	}
	var err error
	if len(gone) > 0 {
		err = idx.Client.Remove(gone...)
	}
	unlock()
	metrics.ObserveCacheDel(idx.name, idx.cacheProvider, float64(released), time.Since(start))
	if err != nil {
		logger.Error("reap remove error", logging.Pairs{keys.CacheName: idx.name, keys.Error: err})
	}
}

// Stop the indexed cache, flush its state, and close the underlying cache
func (idx *IndexedClient) Close() error {
	idx.cancel() // stop the workers
	idx.isClosing.Store(true)
	idx.wg.Wait() // wait for the worker goroutines to exit
	if idx.journal != nil {
		now := time.Now()
		if err := idx.journal.close(now, idx.each); err != nil {
			logger.Warn("unable to persist cache index on close",
				logging.Pairs{keys.CacheName: idx.name, keys.Detail: err.Error()})
		} else {
			idx.lastFlush.Store(now.UnixNano())
		}
	}
	idx.Clear()
	return idx.Client.Close()
}

// tells a test that waits on done that a worker has finished a pass
func signal(done chan bool) {
	select {
	case done <- true:
	default:
		// drop message if no listener
	}
}

func (idx *IndexedClient) flusher(ctx context.Context) {
	defer idx.wg.Done()
	safego.Run(idx.workerPanicHandler(workerFlusher, &idx.flusherExited), func() {
	FLUSHER:
		for {
			fi := idx.options.Load().FlushInterval
			select {
			case <-ctx.Done():
				break FLUSHER
			case <-time.After(time.Duration(fi)):
			case <-idx.forceFlush:
			}
			idx.flushOnce()
			signal(idx.hasFlushed)
		}
		idx.flusherExited.Store(true)
	})
}

// workerPanicHandler returns a safego.PanicHandler that logs, increments
// CacheIndexPanicRecovered{worker}, and flips exited so the health
// endpoint can surface the dead worker.
func (idx *IndexedClient) workerPanicHandler(worker string, exited *atomic.Bool) safego.PanicHandler {
	return func(r any, stack []byte) {
		logger.Error("cache index "+worker+" panic", logging.Pairs{
			"cacheName": idx.name,
			"worker":    worker,
			"panic":     r,
			"stack":     string(stack),
		})
		gm.CacheIndexPanicRecovered.WithLabelValues(worker).Inc()
		exited.Store(true)
	}
}

// a journal that has grown large, or lost records, is replaced by a snapshot of the index
func (idx *IndexedClient) flushOnce() {
	if idx.journal == nil {
		return
	}
	now := time.Now()
	compact, err := idx.journal.flush(now)
	if err != nil {
		logger.Warn("unable to persist cache index changes",
			logging.Pairs{keys.CacheName: idx.name, keys.Detail: err.Error()})
	}
	if compact {
		metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, eventIndex, reasonCompaction)
		if err = idx.journal.compact(now, idx.each); err != nil {
			logger.Warn("unable to persist cache index",
				logging.Pairs{keys.CacheName: idx.name, keys.Detail: err.Error()})
		}
	}
	if err == nil {
		idx.lastFlush.Store(now.UnixNano())
	}
}
