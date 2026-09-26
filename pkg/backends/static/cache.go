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

package static

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

// entryOverheadBytes is a deliberately high estimate of the memory an entry
// uses beyond its body and key: its metadata, index slots and share of a watch.
const entryOverheadBytes = 1024

type fileMeta struct {
	info         os.FileInfo
	modTime      time.Time
	size         int64
	etag         string
	weakETag     string
	contentType  string
	cacheControl string
	compressible bool
}

// an entry's content is immutable once stored, so readers share it without locking
type entry struct {
	fileMeta
	body []byte
	// key is the file's path, which a rendition's own cache key extends
	key      string
	encoding providers.Provider
	cost     int64
	// used marks an entry read since eviction last passed it, which spares it once
	used atomic.Bool
	// unencodable is a bitmap of the encodings found not to shrink the file, which aren't
	// tried again. One that fails says nothing of the others, which are still worth trying.
	unencodable atomic.Uint32
	// node, prev and next place the entry in the index and eviction ring, and chosen marks it
	// while eviction decides whether to go ahead; they require mtx
	node       *dirNode
	prev, next *entry
	chosen     bool
}

// unhelpful returns the encodings found not to shrink the file
func (e *entry) unhelpful() providers.Provider {
	return providers.Provider(e.unencodable.Load()) // #nosec G115 -- only provider bits are ever stored
}

// renditions are the encodings a file may be held in, besides as it is stored
var renditions = []providers.Provider{
	providers.Zstandard, providers.Brotli, providers.GZip, providers.Deflate,
}

// renditionKey is the key a file's rendition is indexed under. The separator can't occur
// in a request path, so it never collides with the key of a file that is named alike.
func renditionKey(key string, enc providers.Provider) string {
	if enc == providers.Identity {
		return key
	}
	return key + "\x00" + enc.String()
}

// cache events, as reported in metrics
const (
	eventEviction     = "eviction"
	eventInvalidation = "invalidation"
)

// cacheMetrics holds a cache's series, resolved once to keep label lookups off the request path
type cacheMetrics struct {
	evictions, invalidations prometheus.Counter
	// objects and bytes are nil unless this cache is the one that publishes them
	objects, bytes prometheus.Gauge
}

// newCacheMetrics returns counters that are private until the cache goes into service
func newCacheMetrics() *cacheMetrics {
	return &cacheMetrics{evictions: unpublished(), invalidations: unpublished()}
}

// gaugeOwners maps a backend's name to the one cache that publishes its gauges. A reload builds
// a cache of the same name while the last is still draining, and only one can speak for the name.
var gaugeOwners sync.Map

// dirWatcher is the part of a filesystem watcher the cache drives
type dirWatcher interface {
	Watch(dir string)
	Unwatch(dir string)
}

// dirNode is one directory in the cache's index of what it holds, which lets a change
// reach exactly the files it affects (one file, or one subtree) and nothing else.
type dirNode struct {
	parent   *dirNode
	name     string
	children map[string]*dirNode
	// held and pending are the keys of the directory's own entries and loads in progress
	held    map[string]*entry
	pending map[string]*reservation
	// osDir is the directory's path for the watcher, watched while it has files
	osDir   string
	watched bool
}

func (n *dirNode) files() int {
	return len(n.held) + len(n.pending)
}

// fileCache holds small files in memory. Reads are lock-free; a file that changes on
// disk is dropped rather than updated in place, and the least recently used make room.
type fileCache struct {
	// entries holds a map per rendition, each of key (root-relative slash path) -> *entry,
	// so that probing for a file's renditions reuses its key rather than building others
	entries [maxRendition + 1]sync.Map
	// size and files include reservations for loads still in progress
	size  atomic.Int64
	files atomic.Int64
	// active is false until a watcher is running, as unwatched entries would go stale
	active      atomic.Bool
	maxFileSize int64
	maxSize     int64
	maxFiles    int64

	// mtx guards the index and every change to entries. Never taken on the read path.
	mtx     sync.Mutex
	root    *dirNode
	watcher dirWatcher
	// hand is where eviction resumes in the ring of held entries, which is ringLen long
	hand    *entry
	ringLen int
	name    string
	metrics *cacheMetrics
	// invalidated counts the entries dropped since the count was last published
	invalidated int
}

func newFileCache(name string, maxFileSize, maxSize, maxFiles int64, w dirWatcher) *fileCache {
	return &fileCache{
		maxFileSize: maxFileSize, maxSize: maxSize, maxFiles: maxFiles, name: name,
		root: &dirNode{}, watcher: w, metrics: newCacheMetrics(),
	}
}

// activate begins holding files, and takes over the publishing of the backend's gauges. It is
// only called for a cache that is going into service, so one built to be validated publishes nothing.
func (c *fileCache) activate() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	gaugeOwners.Store(c.name, c)
	c.metrics.evictions = metrics.FileserverCacheEvents.WithLabelValues(c.name, eventEviction)
	c.metrics.invalidations = metrics.FileserverCacheEvents.WithLabelValues(c.name, eventInvalidation)
	metrics.FileserverCacheMaxObjects.WithLabelValues(c.name).Set(float64(c.maxFiles))
	metrics.FileserverCacheMaxBytes.WithLabelValues(c.name).Set(float64(c.maxSize))
	c.metrics.objects = metrics.FileserverCacheObjects.WithLabelValues(c.name)
	c.metrics.bytes = metrics.FileserverCacheBytes.WithLabelValues(c.name)
	c.active.Store(true)
	c.report()
}

// retire stops holding files and gives up the gauges, so that nothing still draining from this
// cache can publish over its replacement. The series go with it unless a replacement has them.
func (c *fileCache) retire() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.active.Store(false)
	c.metrics.objects, c.metrics.bytes = nil, nil
	if gaugeOwners.CompareAndDelete(c.name, c) {
		metrics.FileserverCacheObjects.DeleteLabelValues(c.name)
		metrics.FileserverCacheBytes.DeleteLabelValues(c.name)
		metrics.FileserverCacheMaxObjects.DeleteLabelValues(c.name)
		metrics.FileserverCacheMaxBytes.DeleteLabelValues(c.name)
	}
}

func (c *fileCache) get(key string, enc providers.Provider) *entry {
	if c == nil || !c.active.Load() {
		return nil
	}
	if v, ok := c.entries[enc].Load(key); ok {
		e := v.(*entry)
		// marking is the only write a hit makes, and it is skipped once marked
		if !e.used.Load() {
			e.used.Store(true)
		}
		return e
	}
	return nil
}

func entryCost(key string, size int64) int64 {
	return size + int64(len(key)) + entryOverheadBytes
}

// admits cheaply reports whether a file of the given size may be held. A full
// cache still admits, as reserve evicts to make room.
func (c *fileCache) admits(key string, size int64) bool {
	return c != nil && c.active.Load() && size <= c.maxFileSize && entryCost(key, size) <= c.maxSize
}

// link and unlink require mtx. The ring is circular, and hand is nil only when it is empty.
func (c *fileCache) link(e *entry) {
	c.ringLen++
	if c.hand == nil {
		e.prev, e.next, c.hand = e, e, e
		return
	}
	// placed just behind the hand, so it is the last entry eviction will reach
	e.next, e.prev = c.hand, c.hand.prev
	e.prev.next, e.next.prev = e, e
}

func (c *fileCache) unlink(e *entry) {
	c.ringLen--
	if e.next == e {
		c.hand = nil
	} else {
		e.prev.next, e.next.prev = e.next, e.prev
		if c.hand == e {
			c.hand = e.next
		}
	}
	e.prev, e.next = nil, nil
}

// evictionBudget is the most entries a request will pass over without evicting them. Without
// it, a cache full of recently used entries would have a request look at every one of them.
// The entries it does evict need no budget, as they are in proportion to the file they admit.
const evictionBudget = 128

// maxEvictions is the most entries one file will be admitted at the cost of, which with
// evictionBudget bounds everything a request does to the cache before it is served. An entry's
// watch is let go of by the watcher in its own time, so an eviction makes no call to the platform.
const maxEvictions = 128

// makeRoom requires mtx. It evicts the least recently used entries to fit cost, and only if
// that is enough to: it chooses what would go first, and takes nothing if room can't be made, so
// that a file that isn't admitted costs the cache nothing it holds. pin is never chosen.
func (c *fileCache) makeRoom(cost int64, pin *entry) bool {
	files, size := c.files.Load()+1-c.maxFiles, c.size.Load()+cost-c.maxSize
	if files <= 0 && size <= 0 {
		return true
	}
	var chosen []*entry
	budget := evictionBudget
	e := c.hand
	// two laps at most: one in which an entry used since the hand last passed is spared and
	// its mark cleared, and one in which, unread since, it is chosen
	for steps := 2 * c.ringLen; e != nil && steps > 0 && budget >= 0 && (files > 0 || size > 0); steps-- {
		switch {
		case e.chosen:
		case e == pin || e.used.Swap(false):
			budget--
		case len(chosen) == maxEvictions:
			// more would have to go than one file is worth; nothing is taken, as below
			steps = 0
		default:
			e.chosen = true
			chosen = append(chosen, e)
			files--
			size -= e.cost
		}
		e = e.next
	}
	if files > 0 || size > 0 {
		// the marks that were cleared stay cleared, so requests that keep coming for room age
		// the cache toward giving it; but nothing is taken from it for a file it didn't admit
		for _, v := range chosen {
			v.chosen = false
		}
		return false
	}
	c.hand = e
	for _, v := range chosen {
		c.remove(v)
	}
	// published once for the pass, rather than for every entry, as the lock is held throughout
	c.metrics.evictions.Add(float64(len(chosen)))
	c.report()
	return true
}

// remove requires mtx. It drops a held entry and lets its directory settle.
func (c *fileCache) remove(e *entry) {
	n := e.node
	c.discard(e)
	c.settle(n)
}

// discard requires mtx. It drops a held entry without settling its directory or publishing
// the change, which its caller does once for everything it drops.
func (c *fileCache) discard(e *entry) {
	delete(e.node.held, renditionKey(e.key, e.encoding))
	c.entries[e.encoding].Delete(e.key)
	c.unlink(e)
	c.size.Add(-e.cost)
	c.files.Add(-1)
}

// report requires mtx. It publishes usage, if this cache is the one that speaks for its name.
func (c *fileCache) report() {
	if c.metrics.objects == nil {
		return
	}
	c.metrics.objects.Set(float64(c.files.Load()))
	c.metrics.bytes.Set(float64(c.size.Load()))
}

// published requires mtx. It publishes what an invalidation dropped, once for all of it.
func (c *fileCache) published() {
	if c.invalidated > 0 {
		c.metrics.invalidations.Add(float64(c.invalidated))
		c.invalidated = 0
		c.report()
	}
}

// splitKey separates a key into the path of its directory and its own name
func splitKey(key string) (dir, name string) {
	if before, after, ok := strings.CutLast(key, "/"); ok {
		return before, after
	}
	return "", key
}

// node returns the index node for a directory path, optionally creating it.
// It requires mtx, and costs the depth of the path rather than the size of the cache.
func (c *fileCache) node(dir string, create bool) *dirNode {
	n := c.root
	for dir != "" {
		var name string
		name, dir, _ = strings.Cut(dir, "/")
		child := n.children[name]
		if child == nil {
			if !create {
				return nil
			}
			child = &dirNode{parent: n, name: name}
			if n.children == nil {
				n.children = make(map[string]*dirNode)
			}
			n.children[name] = child
		}
		n = child
	}
	return n
}

// settle requires mtx. It stops watching a directory that has no files left,
// and unlinks it and any ancestors that the index no longer needs.
func (c *fileCache) settle(n *dirNode) {
	for n != nil {
		if n.files() == 0 && n.watched {
			c.watcher.Unwatch(n.osDir)
			n.watched = false
		}
		if n.parent == nil || n.files() > 0 || len(n.children) > 0 {
			return
		}
		delete(n.parent.children, n.name)
		n = n.parent
	}
}

// a reservation holds capacity and a directory watch for a file that is about
// to be read, so memory is bounded before the read allocates anything.
type reservation struct {
	c    *fileCache
	node *dirNode
	// key is the index key of the rendition being loaded
	key  string
	cost int64
	// basis, for a rendition, is the entry it is derived from, which must still be held
	basis *entry
	// invalid is set when the file changed after the reservation was made; done
	// once it is committed or cancelled. Both require the cache's mtx.
	invalid bool
	done    bool
	// scheduled is set by the one holder of a shared reservation that goes on to store it
	scheduled atomic.Bool
}

// claim reports whether the caller is the one to schedule the reservation's store. Loads that
// shared one read share its reservation, and only the first of them to ask is told to.
func (r *reservation) claim() bool {
	return r.scheduled.CompareAndSwap(false, true)
}

// reserve returns nil when key can't be held, or already is or is being loaded; otherwise the
// caller must commit or cancel. The directory is watched first, so a later change invalidates it.
func (c *fileCache) reserve(path string, enc providers.Provider, osDir string, size int64,
	basis *entry,
) *reservation {
	if c == nil || size > c.maxFileSize {
		return nil
	}
	key := renditionKey(path, enc)
	cost := entryCost(key, size)
	if cost > c.maxSize {
		return nil
	}
	// reserve is the one use of the lock on a request's path, so it is never waited for:
	// a request that finds it busy is served without being held, which costs only a later load
	if !c.mtx.TryLock() {
		return nil
	}
	defer c.mtx.Unlock()
	if !c.active.Load() || !c.basisUsable(basis) {
		return nil
	}
	dir, _ := splitKey(key)
	// looked up before creating, so that a refusal leaves no empty nodes behind
	if n := c.node(dir, false); n != nil {
		if n.held[key] != nil || n.pending[key] != nil {
			return nil
		}
	}
	// evicted for before the node is found, as eviction may unlink emptied nodes. A rendition's
	// basis is held out of it: evicting the file to hold a rendition that needs it holds neither.
	if !c.makeRoom(cost, basis) {
		return nil
	}
	n := c.node(dir, true)
	r := &reservation{c: c, node: n, key: key, cost: cost, basis: basis}
	if n.pending == nil {
		n.pending = make(map[string]*reservation)
	}
	n.pending[key] = r
	c.size.Add(cost)
	c.files.Add(1)
	c.report()
	if !n.watched {
		n.osDir, n.watched = osDir, true
		c.watcher.Watch(osDir)
	}
	return r
}

// basisHeld requires mtx. A rendition derived from an entry that has since been
// dropped may be of content that has since changed, so it can't be held.
func (c *fileCache) basisHeld(basis *entry) bool {
	if basis == nil {
		return true
	}
	v, ok := c.entries[providers.Identity].Load(basis.key)
	return ok && v.(*entry) == basis
}

// basisUsable requires mtx. A rendition may be begun from an entry that is held or is
// still on its way to being; only once it is held can the rendition be held too.
func (c *fileCache) basisUsable(basis *entry) bool {
	if c.basisHeld(basis) {
		return true
	}
	dir, _ := splitKey(basis.key)
	if n := c.node(dir, false); n != nil {
		r := n.pending[basis.key]
		return r != nil && !r.invalid
	}
	return false
}

// finish requires mtx. It ends the reservation, reporting whether it was still open.
func (r *reservation) finish() bool {
	if r.done {
		return false
	}
	r.done = true
	delete(r.node.pending, r.key)
	return true
}

func (r *reservation) cancel() {
	c := r.c
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if r.finish() {
		c.release(r.node, r.cost)
	}
}

// commit stores e, handing it the capacity it needs of what was reserved. It fails, releasing
// the reservation, if the file was invalidated, its basis dropped, or the cache stopped.
func (r *reservation) commit(e *entry) bool {
	c := r.c
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if !r.finish() {
		return false
	}
	if r.invalid || !c.active.Load() || !c.basisHeld(r.basis) {
		c.release(r.node, r.cost)
		return false
	}
	e.cost = min(r.cost, entryCost(r.key, int64(len(e.body))))
	c.size.Add(e.cost - r.cost)
	e.node = r.node
	if r.node.held == nil {
		r.node.held = make(map[string]*entry)
	}
	r.node.held[r.key] = e
	c.link(e)
	c.entries[e.encoding].Store(e.key, e)
	c.report()
	return true
}

// release requires mtx. It returns capacity and lets the directory settle.
func (c *fileCache) release(n *dirNode, cost int64) {
	c.size.Add(-cost)
	c.files.Add(-1)
	c.report()
	c.settle(n)
}

// drop requires mtx. It removes what is held or being loaded under one cache key
// in n, without settling n, so that a caller walking the index can do so afterward.
func (c *fileCache) drop(n *dirNode, key string) {
	// a load in progress keeps its capacity until its loader finishes, as its body
	// may already be allocated; it is only barred from being stored
	if r := n.pending[key]; r != nil {
		r.invalid = true
	}
	if e := n.held[key]; e != nil {
		c.discard(e)
		c.invalidated++
	}
}

// dropFile requires mtx. It drops a file along with every rendition of it.
func (c *fileCache) dropFile(n *dirNode, key string) {
	c.drop(n, key)
	for _, enc := range renditions {
		c.drop(n, renditionKey(key, enc))
	}
}

// dropTree requires mtx. It visits only n's subtree.
func (c *fileCache) dropTree(n *dirNode) {
	for _, child := range n.children {
		c.dropTree(child)
	}
	for key := range n.held {
		c.drop(n, key)
	}
	for key := range n.pending {
		c.drop(n, key)
	}
	c.settle(n)
}

// invalidate drops the one file at key, in every rendition
func (c *fileCache) invalidate(key string) {
	if c == nil {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.invalidateFile(key)
	c.published()
}

func (c *fileCache) invalidateFile(key string) {
	dir, _ := splitKey(key)
	if n := c.node(dir, false); n != nil {
		c.dropFile(n, key)
		c.settle(n)
	}
}

// invalidatePath applies a change to the path at key, which may name a file, or
// a directory that takes everything beneath it along. Nothing else is visited.
func (c *fileCache) invalidatePath(key string) {
	if c == nil {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.invalidateFile(key)
	if n := c.node(key, false); n != nil {
		c.dropTree(n)
	}
	c.published()
}

func (c *fileCache) purge() {
	if c == nil {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.dropTree(c.root)
	c.published()
}

// revalidate drops every file for which stale returns true. stale may block on the filesystem,
// so it is called without mtx, and once per file however many renditions of it are held.
func (c *fileCache) revalidate(stale func(key string, e *entry) bool) {
	if c == nil {
		return
	}
	for i := range c.entries {
		c.entries[i].Range(func(_, v any) bool {
			e := v.(*entry)
			// a file is checked through the first of its entries: as stored where that is held,
			// which it may not be, as the stored file is evicted apart from its renditions
			for earlier := range i {
				if _, ok := c.entries[earlier].Load(e.key); ok {
					return true
				}
			}
			if stale(e.key, e) {
				c.invalidate(e.key)
			}
			return true
		})
	}
}
