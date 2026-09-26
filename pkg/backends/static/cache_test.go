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
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeWatcher struct {
	mtx  sync.Mutex
	dirs map[string]bool
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{dirs: make(map[string]bool)}
}

func (w *fakeWatcher) Watch(dir string) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	w.dirs[dir] = true
}

func (w *fakeWatcher) Unwatch(dir string) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	delete(w.dirs, dir)
}

func (w *fakeWatcher) watched() int {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	return len(w.dirs)
}

func (c *fileCache) count() int {
	var n int
	if c != nil {
		for i := range c.entries {
			c.entries[i].Range(func(_, _ any) bool { n++; return true })
		}
	}
	return n
}

// nodes counts the index's directories below its root
func (c *fileCache) nodes() int {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	var walk func(n *dirNode) int
	walk = func(n *dirNode) int {
		total := len(n.children)
		for _, child := range n.children {
			total += walk(child)
		}
		return total
	}
	return walk(c.root)
}

// ring counts the entries eviction can reach, which must be exactly those held
func (c *fileCache) ring() int {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.hand == nil {
		return 0
	}
	n := 1
	for e := c.hand.next; e != c.hand; e = e.next {
		n++
	}
	return n
}

var testNames atomic.Int64

// testName is unique to each run of a test, as a backend's metrics are series that are
// shared by everything of its name and would otherwise carry over between runs
func testName(t testing.TB) string {
	return t.Name() + "#" + strconv.FormatInt(testNames.Add(1), 10)
}

func newTestCache(t *testing.T, maxFileSize, maxSize, maxFiles int64) (*fileCache, *fakeWatcher) {
	w := newFakeWatcher()
	c := newFileCache(testName(t), maxFileSize, maxSize, maxFiles, w)
	c.activate()
	t.Cleanup(c.retire)
	return c, w
}

func testEntry(key, body string) *entry {
	return &entry{key: key, body: []byte(body)}
}

// hold reserves and commits a body under key in dir, as a completed load does
func (c *fileCache) hold(key, dir, body string) bool {
	rsv := c.reserve(key, providers.Identity, dir, int64(len(body)), nil)
	if rsv == nil {
		return false
	}
	return rsv.commit(testEntry(key, body))
}

func (c *fileCache) held(key string) *entry {
	return c.get(key, providers.Identity)
}

func TestFileCacheNilIsInert(t *testing.T) {
	var c *fileCache
	if c.held("a") != nil || c.admits("a", 1) || c.count() != 0 ||
		c.reserve("a", providers.Identity, "/d", 1, nil) != nil {
		t.Error("expected a nil cache to hold nothing")
	}
	c.invalidate("a")
	c.invalidatePath("a")
	c.purge()
	c.revalidate(func(string, *entry) bool { return true })
}

func TestFileCacheInactive(t *testing.T) {
	c := newFileCache(testName(t), 10, 10000, 10, newFakeWatcher())
	if c.admits("a", 1) || c.hold("a", "/d", "a") || c.held("a") != nil {
		t.Error("expected an inactive cache to hold nothing")
	}
}

func TestFileCacheAccounting(t *testing.T) {
	c, _ := newTestCache(t, 10, 2*entryCost("a", 8), 10)
	if !c.admits("a", 10) || c.admits("a", 11) || c.reserve("a", providers.Identity, "/d", 11, nil) != nil {
		t.Error("expected admission to follow the per-file limit")
	}
	tiny, _ := newTestCache(t, 10, entryCost("a", 1)-1, 10)
	if tiny.admits("a", 1) || tiny.hold("a", "/d", "1") {
		t.Error("expected a file that could never fit not to be admitted")
	}
	if !c.hold("a", "/d", "12345678") {
		t.Fatal("expected the entry to be stored")
	}
	if c.hold("a", "/d", "1") {
		t.Error("expected a held key not to be replaced")
	}
	// overhead is charged per entry, so even an empty file takes capacity
	if !c.hold("b", "/d", "") || c.size.Load() != entryCost("a", 8)+entryCost("b", 0) {
		t.Errorf("expected an empty file to be charged its overhead, size is %d", c.size.Load())
	}
	if c.files.Load() != 2 || c.count() != 2 || c.ring() != 2 {
		t.Errorf("expected 2 files, got %d", c.files.Load())
	}
	if got := testutil.ToFloat64(c.metrics.bytes); got != float64(c.size.Load()) {
		t.Errorf("expected the usage metric to follow the accounting, got %v", got)
	}
	c.purge()
	if c.size.Load() != 0 || c.files.Load() != 0 || c.ring() != 0 || testutil.ToFloat64(c.metrics.objects) != 0 {
		t.Errorf("expected accounting to return to zero, got %d bytes %d files", c.size.Load(), c.files.Load())
	}
}

func TestFileCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c, w := newTestCache(t, 10, 1<<30, 3)
	for _, dir := range []string{"a", "b", "c"} {
		c.hold(dir+"/f", "/r/"+dir, "x")
	}
	// every entry starts unread; a and c are read, so b is the least recently used
	c.held("a/f")
	c.held("c/f")
	if !c.hold("d/f", "/r/d", "x") {
		t.Fatal("expected a full cache to make room")
	}
	if c.held("b/f") != nil || c.count() != 3 || c.files.Load() != 3 || c.ring() != 3 {
		t.Errorf("expected b to be evicted, %d entries remain", c.count())
	}
	if w.watched() != 3 || c.nodes() != 3 {
		t.Errorf("expected the evicted entry's directory to be released, %d watched", w.watched())
	}
	if got := testutil.ToFloat64(c.metrics.evictions); got != 1 {
		t.Errorf("expected 1 eviction to be counted, got %v", got)
	}
	// having all been read, each is spared once; the hand then evicts as it goes
	for _, dir := range []string{"a", "c", "d"} {
		c.held(dir + "/f")
	}
	c.hold("e/f", "/r/e", "x")
	if c.count() != 3 || c.held("e/f") == nil {
		t.Error("expected room to be made even when every entry was recently used")
	}
}

func TestFileCacheEvictsForBytes(t *testing.T) {
	c, _ := newTestCache(t, 100, 3*entryCost("a", 50), 100)
	for _, key := range []string{"a", "b", "c"} {
		c.hold(key, "/d", string(make([]byte, 50)))
	}
	// a larger file needs more than one entry's room
	big := string(make([]byte, 100))
	if !c.hold("d", "/d", big) || c.size.Load() > c.maxSize {
		t.Fatalf("expected room for a larger file, size is %d of %d", c.size.Load(), c.maxSize)
	}
	if c.held("d") == nil || c.count() != 2 {
		t.Errorf("expected 2 entries after evicting for a larger one, got %d", c.count())
	}
	// loads in progress can't be evicted, so what they hold is not available
	c.purge()
	pending := c.reserve("p", providers.Identity, "/d", 100, nil)
	pending2 := c.reserve("q", providers.Identity, "/d", 100, nil)
	if pending == nil || pending2 == nil || c.hold("r", "/d", big) {
		t.Error("expected capacity held by loads in progress not to be evictable")
	}
	pending.cancel()
	if !c.hold("r", "/d", big) {
		t.Error("expected released capacity to be reusable")
	}
	pending2.cancel()
}

func TestFileCacheReservation(t *testing.T) {
	c, w := newTestCache(t, 10, 10000, 10)
	// capacity is taken when the load begins, before its body exists
	rsv := c.reserve("a", providers.Identity, "/d", 8, nil)
	if rsv == nil || c.size.Load() != entryCost("a", 8) || c.files.Load() != 1 || w.watched() != 1 {
		t.Fatal("expected a reservation to take capacity and a watch")
	}
	if c.reserve("a", providers.Identity, "/d", 8, nil) != nil {
		t.Error("expected a file that is already being loaded not to be reserved twice")
	}
	rsv.cancel()
	rsv.cancel() // finishing twice must not release twice
	if rsv.commit(testEntry("a", "late")) {
		t.Error("expected a cancelled reservation not to commit")
	}
	if c.size.Load() != 0 || c.files.Load() != 0 || w.watched() != 0 || c.nodes() != 0 {
		t.Error("expected a cancelled reservation to release everything")
	}
	// a load that began before its file changed must not land after it
	rsv = c.reserve("a", providers.Identity, "/d", 5, nil)
	c.invalidate("a")
	if c.size.Load() == 0 {
		t.Error("expected a load in progress to keep its capacity until its loader finishes")
	}
	if rsv.commit(testEntry("a", "stale")) || c.held("a") != nil || c.size.Load() != 0 || w.watched() != 0 {
		t.Error("expected a load that raced an invalidation to be discarded")
	}
	if !c.hold("a", "/d", "fresh") || string(c.held("a").body) != "fresh" {
		t.Error("expected a load after the invalidation to be stored")
	}
	if c.reserve("a", providers.Identity, "/d", 5, nil) != nil {
		t.Error("expected a held file not to be reserved")
	}
	// capacity reserved for more than the body turned out to need is handed back
	rsv = c.reserve("s", providers.Identity, "/d", 10, nil)
	before := c.size.Load()
	if !rsv.commit(testEntry("s", "12")) || c.size.Load() != before-8 {
		t.Errorf("expected the unused reservation to be released, size went %d to %d", before, c.size.Load())
	}
	// so must one that raced the cache being stopped
	rsv = c.reserve("b", providers.Identity, "/d", 1, nil)
	c.active.Store(false)
	if rsv.commit(testEntry("b", "b")) || c.reserve("c", providers.Identity, "/d", 1, nil) != nil {
		t.Error("expected a load that raced a stop to be discarded")
	}
}

func TestFileCacheRenditions(t *testing.T) {
	c, _ := newTestCache(t, 100, 1<<20, 100)
	c.hold("site/app.js", "/r/site", "identity body")
	// a real file named like a rendition is a different key entirely
	c.hold("site/app.js.gzip", "/r/site", "a file")
	basis := c.held("site/app.js")
	rsv := c.reserve("site/app.js", providers.GZip, "/r/site", 13, basis)
	if rsv == nil || !rsv.commit(&entry{key: "site/app.js", encoding: providers.GZip, body: []byte("gz")}) {
		t.Fatal("expected the rendition to be held")
	}
	if v := c.get("site/app.js", providers.GZip); v == nil || string(v.body) != "gz" {
		t.Fatal("expected the rendition under its own key")
	}
	if c.get("site/app.js", providers.Brotli) != nil || string(c.held("site/app.js").body) != "identity body" {
		t.Error("expected other renditions to be unaffected")
	}
	if c.count() != 3 || c.nodes() != 1 {
		t.Errorf("expected 3 entries in one directory, got %d in %d", c.count(), c.nodes())
	}
	// a rendition of an entry that is no longer the one held may be of other content
	stale := c.reserve("site/app.js", providers.Brotli, "/r/site", 13, basis)
	c.invalidate("site/app.js")
	if c.get("site/app.js", providers.GZip) != nil || c.held("site/app.js") != nil {
		t.Error("expected a changed file to take every rendition along")
	}
	if c.held("site/app.js.gzip") == nil {
		t.Error("expected the file named like a rendition to be untouched")
	}
	c.hold("site/app.js", "/r/site", "new identity")
	if stale.commit(&entry{key: "site/app.js", encoding: providers.Brotli, body: []byte("br")}) {
		t.Error("expected a rendition of replaced content to be refused")
	}
	if c.reserve("site/app.js", providers.Zstandard, "/r/site", 13, basis) != nil {
		t.Error("expected no reservation against an entry that is no longer held")
	}
	// revalidation drops a stale file through any one of its entries
	fresh := c.held("site/app.js")
	rsv = c.reserve("site/app.js", providers.Zstandard, "/r/site", 13, fresh)
	rsv.commit(&entry{key: "site/app.js", encoding: providers.Zstandard, body: []byte("zs")})
	c.revalidate(func(key string, _ *entry) bool { return key == "site/app.js" })
	if c.count() != 1 {
		t.Errorf("expected only the unrelated file to remain, got %d", c.count())
	}
}

func TestFileCacheRevalidatesEachFileOnce(t *testing.T) {
	c, _ := newTestCache(t, 100, 1<<20, 100)
	rendition := func(key string, enc providers.Provider) {
		rsv := c.reserve(key, enc, "/r", 5, c.held(key))
		if rsv == nil || !rsv.commit(&entry{key: key, encoding: enc, body: []byte("x")}) {
			t.Fatalf("expected the %s rendition of %s to be held", enc, key)
		}
	}
	// one file held every way it can be, one only as stored, and one only as renditions
	for _, key := range []string{"all.js", "stored.js", "orphan.js"} {
		c.hold(key, "/r", "stored")
	}
	for _, enc := range renditions {
		rendition("all.js", enc)
	}
	rendition("orphan.js", providers.Brotli)
	rendition("orphan.js", providers.Deflate)
	// the stored file is evicted apart from its renditions, which must still be checked
	c.mtx.Lock()
	c.remove(c.held("orphan.js"))
	c.mtx.Unlock()
	if c.count() != 8 {
		t.Fatalf("expected 8 entries, got %d", c.count())
	}
	checked := make(map[string]int)
	c.revalidate(func(key string, _ *entry) bool {
		checked[key]++
		return false
	})
	if len(checked) != 3 || checked["all.js"] != 1 || checked["stored.js"] != 1 || checked["orphan.js"] != 1 {
		t.Errorf("expected each file to be checked once, however it is held, got %v", checked)
	}
	// and a stale one goes in every rendition, through whichever of its entries was checked
	c.revalidate(func(key string, _ *entry) bool { return key != "stored.js" })
	if c.count() != 1 || c.held("stored.js") == nil {
		t.Errorf("expected only the file that wasn't stale to remain, got %d", c.count())
	}
}

func TestFileCacheDeclinedAdmissionTakesNothing(t *testing.T) {
	const held = 4 * evictionBudget
	c, _ := newTestCache(t, 1<<20, 1<<40, held)
	for i := range held {
		c.hold(strconv.Itoa(i), "/r", "x")
	}
	// every entry has been read, so more of them would have to be passed over than a request will
	for i := range held {
		c.held(strconv.Itoa(i))
	}
	if c.hold("new", "/r", "x") {
		t.Fatal("expected a request not to pass over more entries than its budget")
	}
	if c.count() != held || c.ring() != held || testutil.ToFloat64(c.metrics.evictions) != 0 {
		t.Errorf("expected a declined admission to have taken nothing, %d of %d remain", c.count(), held)
	}
	// it aged the entries it passed, though, so a request that comes again is admitted
	if !c.hold("new", "/r", "x") || c.count() != held || testutil.ToFloat64(c.metrics.evictions) != 1 {
		t.Errorf("expected the next request to be admitted for one eviction, got %v evictions",
			testutil.ToFloat64(c.metrics.evictions))
	}
	// unless those entries were read in between, as ones in use are
	c.purge()
	for i := range held {
		c.hold(strconv.Itoa(i), "/r", "x")
	}
	for range 3 {
		for i := range held {
			c.held(strconv.Itoa(i))
		}
		if c.hold("other", "/r", "x") || c.count() != held {
			t.Fatal("expected a cache in constant use to be left as it is")
		}
	}
}

func TestFileCacheLargeFileTakesNothingUnlessAdmitted(t *testing.T) {
	each := entryCost("1000", 8)
	c, _ := newTestCache(t, 1<<30, 1000*each, 2000)
	for i := range 1000 {
		c.hold(strconv.Itoa(1000+i), "/r", "12345678")
	}
	// a larger file that many unread small ones make way for is admitted in one pass
	if !c.hold("big", "/r", string(make([]byte, (maxEvictions-10)*each))) || c.size.Load() > c.maxSize {
		t.Fatal("expected room to be made for a large file among many unread small ones")
	}
	evicted := testutil.ToFloat64(c.metrics.evictions)
	if evicted < maxEvictions-10 || evicted > maxEvictions {
		t.Errorf("expected about %d entries to make way, got %v", maxEvictions-10, evicted)
	}
	if got := testutil.ToFloat64(c.metrics.objects); got != float64(c.files.Load()) {
		t.Errorf("expected usage to be published as it stands after the pass, got %v", got)
	}
	// one that more entries would have to make way for than a request will evict takes nothing
	before := c.count()
	if c.hold("vast", "/r", string(make([]byte, (maxEvictions+50)*each))) {
		t.Fatal("expected a file that too many entries would make way for to be declined")
	}
	if c.count() != before || testutil.ToFloat64(c.metrics.evictions) != evicted {
		t.Errorf("expected a declined file to have evicted nothing, %d of %d remain", c.count(), before)
	}
	// nor does one that can't be fitted because too much of the cache is in use
	for i := range 1000 {
		c.held(strconv.Itoa(1000 + i))
	}
	c.held("big")
	if c.hold("other", "/r", string(make([]byte, 20*each))) || c.count() != before {
		t.Errorf("expected a file that there isn't room for to take nothing, %d of %d remain", c.count(), before)
	}
	// with the capacity taken by loads in progress, there is nothing to evict at all
	c.purge()
	a := c.reserve("a", providers.Identity, "/r", 600*each, nil)
	if a == nil || c.reserve("b", providers.Identity, "/r", 600*each, nil) != nil {
		t.Error("expected a reservation that nothing can be evicted for to be declined")
	}
	a.cancel()
}

func TestFileCacheRenditionNeverEvictsItsBasis(t *testing.T) {
	rendition := func(c *fileCache, key string, size int64) bool {
		rsv := c.reserve(key, providers.GZip, "/r", size, c.held(key))
		return rsv != nil && rsv.commit(&entry{key: key, encoding: providers.GZip, body: make([]byte, size)})
	}
	// room for one object: the file is what is kept, not a rendition that would need it
	c, _ := newTestCache(t, 100, 1<<20, 1)
	c.hold("app.js", "/r", "stored")
	for range 3 {
		if rendition(c, "app.js", 3) {
			t.Fatal("expected a rendition that can't be held beside its file to be declined")
		}
		if c.held("app.js") == nil || c.count() != 1 || testutil.ToFloat64(c.metrics.evictions) != 0 {
			t.Fatal("expected the file to be kept, rather than evicted for a rendition that needs it")
		}
	}
	// room for two, with the file at the hand, where it would be the first to go
	c, _ = newTestCache(t, 100, 1<<20, 2)
	c.hold("app.js", "/r", "stored")
	c.hold("other.js", "/r", "stored")
	if c.hand != c.held("app.js") {
		t.Fatal("expected the file to be where eviction resumes")
	}
	if !rendition(c, "app.js", 3) || c.held("app.js") == nil || c.held("other.js") != nil {
		t.Error("expected the other entry to make way, and the file and its rendition to be held")
	}
	// and the same by bytes: only the file could make enough room, so the rendition is declined
	c, _ = newTestCache(t, 100, entryCost("app.js", 50)+entryCost(renditionKey("app.js", providers.GZip), 20)-1, 10)
	c.hold("app.js", "/r", string(make([]byte, 50)))
	if rendition(c, "app.js", 20) || c.held("app.js") == nil {
		t.Error("expected the file to be kept when only it could make room for its rendition")
	}
}

func TestFileCacheGaugesHaveOneOwner(t *testing.T) {
	name := testName(t)
	gauges := []*prometheus.GaugeVec{
		metrics.FileserverCacheObjects, metrics.FileserverCacheBytes,
		metrics.FileserverCacheMaxObjects, metrics.FileserverCacheMaxBytes,
	}
	published := func() bool {
		// deleting reports whether there was a series to delete, which is put back if so
		var found bool
		for _, g := range gauges {
			if v := testutil.ToFloat64(g.WithLabelValues(name)); g.DeleteLabelValues(name) && v != 0 {
				found = true
				g.WithLabelValues(name).Set(v)
			}
		}
		return found
	}
	// a cache built to validate a configuration, and then not used, publishes nothing
	rejected := newFileCache(name, 10, 1<<20, 7, newFakeWatcher())
	if rejected.hold("a", "/r", "x") || published() {
		t.Error("expected a cache that never went into service to publish nothing")
	}

	first := newFileCache(name, 10, 1<<20, 7, newFakeWatcher())
	first.activate()
	first.hold("a", "/r", "x")
	draining := first.reserve("b", providers.Identity, "/r", 1, nil)
	if got := testutil.ToFloat64(metrics.FileserverCacheObjects.WithLabelValues(name)); got != 2 {
		t.Fatalf("expected the cache in service to publish its usage, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.FileserverCacheMaxObjects.WithLabelValues(name)); got != 7 {
		t.Fatalf("expected the cache in service to publish its limits, got %v", got)
	}

	// a reload replaces it with a cache of the same name while it is still draining
	first.retire()
	second := newFileCache(name, 10, 1<<20, 9, newFakeWatcher())
	second.activate()
	for _, key := range []string{"a", "b", "c"} {
		second.hold(key, "/r", "x")
	}
	draining.cancel()
	first.purge()
	if got := testutil.ToFloat64(metrics.FileserverCacheObjects.WithLabelValues(name)); got != 3 {
		t.Errorf("expected a retired cache not to publish over its replacement, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.FileserverCacheMaxObjects.WithLabelValues(name)); got != 9 {
		t.Errorf("expected the replacement's limits, got %v", got)
	}
	// retired again, as a rollback may do, it still leaves its replacement's series alone
	first.retire()
	if !published() {
		t.Error("expected the replacement's series to have survived the retired cache")
	}
	// with no cache left for the name (it was disabled, or the backend removed), the series go
	second.retire()
	if published() {
		t.Error("expected the series to be removed with the last cache of the name")
	}
	// and a cache restored by a rollback publishes again
	first.activate()
	defer first.retire()
	if !published() {
		t.Error("expected a cache put back into service to publish")
	}
}

func TestFileCacheInvalidationIsScoped(t *testing.T) {
	c, _ := newTestCache(t, 10, 1<<20, 100)
	c.hold("site/a.css", "/r/site", "x")
	// loads in progress all over the tree when one unrelated file changes
	sibling := c.reserve("site/b.css", providers.Identity, "/r/site", 1, nil)
	elsewhere := c.reserve("other/deep/c.css", providers.Identity, "/r/other/deep", 1, nil)
	top := c.reserve("index.html", providers.Identity, "/r", 1, nil)
	c.invalidatePath("site/a.css")
	c.invalidatePath("site/never-held.css")
	c.invalidatePath("unknown/dir/file.css")
	if c.held("site/a.css") != nil {
		t.Error("expected the changed file to be dropped")
	}
	if got := testutil.ToFloat64(c.metrics.invalidations); got != 1 {
		t.Errorf("expected 1 invalidation to be counted, got %v", got)
	}
	loads := map[string]*reservation{"site/b.css": sibling, "other/deep/c.css": elsewhere, "index.html": top}
	for key, rsv := range loads {
		if !rsv.commit(testEntry(key, "x")) {
			t.Errorf("expected the load of %s to survive a change to another file", key)
		}
	}

	// a changed directory takes its own subtree, held and in progress, and nothing else
	inTree := c.reserve("other/deep/d.css", providers.Identity, "/r/other/deep", 1, nil)
	outside := c.reserve("site/e.css", providers.Identity, "/r/site", 1, nil)
	lookalike := c.reserve("other2/f.css", providers.Identity, "/r/other2", 1, nil)
	c.invalidatePath("other")
	if c.held("other/deep/c.css") != nil || inTree.commit(testEntry("other/deep/d.css", "x")) {
		t.Error("expected everything under the changed directory to be refused")
	}
	if c.held("site/b.css") == nil || c.held("index.html") == nil ||
		!outside.commit(testEntry("site/e.css", "x")) || !lookalike.commit(testEntry("other2/f.css", "x")) {
		t.Error("expected everything outside the changed directory to be untouched")
	}
	c.purge()
	if c.size.Load() != 0 || c.files.Load() != 0 || c.nodes() != 0 || c.ring() != 0 {
		t.Errorf("expected nothing to remain, got %d files %d nodes", c.files.Load(), c.nodes())
	}
}

func TestFileCacheWatchesFollowEntries(t *testing.T) {
	c, w := newTestCache(t, 10, 1<<20, 100)
	c.hold("a/1", "/root/a", "x")
	c.hold("a/2", "/root/a", "x")
	c.hold("b/1", "/root/b", "x")
	if w.watched() != 2 {
		t.Fatalf("expected one watch per directory, got %d", w.watched())
	}
	c.invalidate("a/1")
	if w.watched() != 2 {
		t.Error("expected a directory to stay watched while it holds an entry")
	}
	c.invalidate("a/2")
	if w.watched() != 1 {
		t.Error("expected a directory holding nothing to be released")
	}
	// a directory that only leads to others is indexed, but has nothing to watch
	c.hold("c/d/e/1", "/root/c/d/e", "x")
	if w.watched() != 2 || c.nodes() != 4 {
		t.Errorf("expected 2 watches over 4 indexed directories, got %d over %d", w.watched(), c.nodes())
	}
	c.invalidate("c/d/e/1")
	if c.nodes() != 1 {
		t.Errorf("expected emptied directories to leave the index, %d remain", c.nodes())
	}
	c.purge()
	if w.watched() != 0 || c.nodes() != 0 {
		t.Error("expected a purge to release every watch")
	}
}

func TestFileCacheTreeAndPurge(t *testing.T) {
	c, _ := newTestCache(t, 10, 1<<20, 100)
	// docs is held as a file here, and is also a directory: a changed path may be either
	for _, key := range []string{"docs", "docs/a", "docs/sub/b", "docs2/c", "d"} {
		c.hold(key, "/d", "x")
	}
	c.invalidatePath("docs")
	if c.count() != 2 || c.held("docs2/c") == nil || c.held("d") == nil {
		t.Errorf("expected only the named tree to be dropped, %d entries remain", c.count())
	}
	c.revalidate(func(key string, _ *entry) bool { return key == "d" })
	if c.count() != 1 || c.held("d") != nil {
		t.Error("expected the stale entry to be dropped")
	}
	c.purge()
	if c.count() != 0 || c.size.Load() != 0 {
		t.Error("expected a purge to drop everything")
	}
}

func TestFileCacheConcurrent(t *testing.T) {
	c, w := newTestCache(t, 10, 1<<20, 3)
	keys := []string{"a", "b", "c", "d", "e", "f"}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 2000 {
				key := keys[(i+j)%len(keys)]
				switch j % 4 {
				case 0:
					c.hold(key, "/d/"+key, "12345")
				case 1:
					c.held(key)
				case 2:
					if e := c.held(key); e != nil {
						if rsv := c.reserve(key, providers.GZip, "/d/"+key, 5, e); rsv != nil {
							rsv.commit(&entry{key: key, encoding: providers.GZip, body: []byte("gz")})
						}
					}
				default:
					c.invalidatePath(key)
				}
				if n := c.files.Load(); n > 3 {
					t.Errorf("expected the file limit to hold under contention, got %d", n)
					return
				}
			}
		})
	}
	wg.Wait()
	if c.ring() != c.count() {
		t.Errorf("expected eviction to reach exactly what is held, %d of %d", c.ring(), c.count())
	}
	c.purge()
	if c.size.Load() != 0 || c.files.Load() != 0 || c.count() != 0 || w.watched() != 0 || c.nodes() != 0 {
		t.Errorf("expected accounting to settle at zero, got %d bytes %d files %d watches %d nodes",
			c.size.Load(), c.files.Load(), w.watched(), c.nodes())
	}
}

func BenchmarkInvalidateFile(b *testing.B) {
	for _, held := range []int{100, 10000} {
		b.Run(strconv.Itoa(held), func(b *testing.B) {
			w := newFakeWatcher()
			c := newFileCache(testName(b), 10, 1<<40, int64(held)+1, w)
			c.activate()
			defer c.retire()
			for i := range held {
				dir := "assets/" + strconv.Itoa(i%100)
				c.hold(dir+"/"+strconv.Itoa(i)+".css", "/r/"+dir, "x")
			}
			for b.Loop() {
				// as a deployment does: a file event for a path that is not a directory
				c.invalidatePath("assets/7/changed.css")
			}
		})
	}
}

func BenchmarkGet(b *testing.B) {
	w := newFakeWatcher()
	c := newFileCache(testName(b), 10, 1<<40, 1000, w)
	c.activate()
	defer c.retire()
	c.hold("assets/app.css", "/r/assets", "x")
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.get("assets/app.css", providers.Identity)
		}
	})
}

func BenchmarkReserveFullRecentlyUsed(b *testing.B) {
	const held = 10000
	c := newFileCache(testName(b), 1<<20, 1<<40, held, newFakeWatcher())
	c.activate()
	defer c.retire()
	for i := range held {
		c.hold(strconv.Itoa(i), "/r", "x")
	}
	var n int
	for b.Loop() {
		b.StopTimer()
		// full again, whatever the last pass evicted, and every entry read since the hand last
		// passed: the most that a reservation can have to do
		for c.files.Load() < held {
			n++
			c.hold("refill"+strconv.Itoa(n), "/r", "x")
		}
		for i := range c.entries {
			c.entries[i].Range(func(_, v any) bool { v.(*entry).used.Store(true); return true })
		}
		b.StartTimer()
		rsv := c.reserve("new", providers.Identity, "/r", 1, nil)
		b.StopTimer()
		if rsv != nil {
			rsv.cancel()
		}
		b.StartTimer()
	}
}

// benchmarkWorstAdmission measures the most a request can do to the cache before it is served:
// pass over a budget of entries in use, and then evict as many as one admission may, each the
// last file in a directory of its own, so that each takes its directory's watch with it
func benchmarkWorstAdmission(b *testing.B, w dirWatcher, dir func(i int) string) {
	const held = 4 * (evictionBudget + maxEvictions)
	each := entryCost("00000/f", 1)
	c := newFileCache(testName(b), 1<<30, held*each, 1<<20, w)
	c.activate()
	defer c.retire()
	key := func(i int) string { return strconv.Itoa(10000+i) + "/f" }
	for b.Loop() {
		b.StopTimer()
		c.purge()
		for i := range held {
			c.hold(key(i), dir(i), "x")
		}
		// the hand starts at the first entry: a budget of used entries, then unused ones to evict
		for i := range evictionBudget - 1 {
			c.get(key(i), providers.Identity)
		}
		b.StartTimer()
		rsv := c.reserve("big", providers.Identity, dir(held), (maxEvictions-1)*each, nil)
		b.StopTimer()
		if rsv == nil {
			b.Fatal("expected the file to be admitted")
		}
		rsv.cancel()
		b.StartTimer()
	}
}

func BenchmarkWorstAdmission(b *testing.B) {
	benchmarkWorstAdmission(b, newFakeWatcher(), func(i int) string { return "/r/" + strconv.Itoa(i) })
}

// BenchmarkWorstAdmissionWatched is the same against the filesystem, with a real watcher
// armed on every directory that an evicted entry was the last file of
func BenchmarkWorstAdmissionWatched(b *testing.B) {
	const held = 4 * (evictionBudget + maxEvictions)
	parent := b.TempDir()
	dirs := make([]string, held+1)
	for i := range dirs {
		dirs[i] = filepath.Join(parent, strconv.Itoa(i))
		if err := os.Mkdir(dirs[i], 0o700); err != nil {
			b.Fatal(err)
		}
	}
	w, err := filesystem.NewDirWatcher(&filesystem.DirOptions{
		Name: b.Name(), Interval: time.Hour, OnEvent: func(string) {},
	})
	if err != nil {
		b.Fatal(err)
	}
	w.Start()
	defer w.Close()
	benchmarkWorstAdmission(b, w, func(i int) string { return dirs[i] })
}
