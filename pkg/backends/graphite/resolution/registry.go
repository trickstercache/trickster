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

package resolution

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

// Store is the subset of cache.Cache the registry persists through.
// A nil Store disables persistence.
type Store interface {
	Store(cacheKey string, data []byte, ttl time.Duration) error
	Retrieve(cacheKey string) ([]byte, status.LookupStatus, error)
}

// RegistryOptions configures a Registry
type RegistryOptions struct {
	// TTL is how long leaf bindings and ladders are trusted
	TTL time.Duration
	// NegativeTTL is the initial backoff of a negative entry; it doubles per
	// consecutive failure up to NegativeTTLMax
	NegativeTTL    time.Duration
	NegativeTTLMax time.Duration
	// MaxEntries bounds each layer
	MaxEntries int
	// KeyPrefix namespaces persisted entries (one registry per backend)
	KeyPrefix string
	// Now is the clock (tests override it)
	Now func() time.Time
}

// Registry is the three-layer resolution registry (leaf -> ladder key ->
// *Ladder, target -> leaf set) plus a negative cache; lookups never block on I/O.
type Registry struct {
	mu       sync.RWMutex
	leaves   map[string]*leafEntry
	ladders  map[string]*ladderEntry
	targets  map[string]*targetEntry
	negative map[string]*negEntry
	gen      atomic.Uint64
	o        RegistryOptions
	store    atomic.Pointer[Store]
	now      func() time.Time
	// complete tracks the number of complete ladders in r.ladders,
	// maintained on every mutation. Written under mu; read under mu.RLock.
	complete int
	// Observer, when set, receives layer-size gauge updates from the mutation
	// that changed them, keeping the resolution hot path free of gauge writes.
	Observer Observer
	// emitters publish layer sizes in mutation order without holding any
	// lock across the Observer callback; see publish.
	emitters [4]layerEmitter
}

// layerEmitter is a single-slot, latest-wins publisher for one layer.
type layerEmitter struct {
	next      uint64 // sequence counter, advanced under Registry.mu
	latest    atomic.Pointer[pendingEmit]
	busy      atomic.Bool
	published atomic.Uint64
}

func layerIndex(layer string) int {
	switch layer {
	case LayerLeaf:
		return 0
	case LayerLadder:
		return 1
	case LayerTarget:
		return 2
	}
	return 3
}

// pendingEmit is a captured (layer, size, sequence) triple to publish after
// the registry lock is released
type pendingEmit struct {
	layer string
	n     int
	seq   uint64
}

// assigns the next publication sequence for a layer; called with mu held
func (r *Registry) captureEmit(layer string, n int) pendingEmit {
	e := &r.emitters[layerIndex(layer)]
	e.next++
	return pendingEmit{layer: layer, n: n, seq: e.next}
}

// Emits the newest captured size via a latest-wins slot and single drainer.
// The callback runs with no lock held, so it may block or re-enter the registry.
func (r *Registry) publish(p pendingEmit) {
	if r.Observer == nil || p.seq == 0 {
		return
	}
	e := &r.emitters[layerIndex(p.layer)]
	for {
		old := e.latest.Load()
		if old != nil && old.seq >= p.seq {
			break
		}
		if e.latest.CompareAndSwap(old, &p) {
			break
		}
	}
	for e.busy.CompareAndSwap(false, true) {
		v := e.latest.Load()
		if v != nil && v.seq > e.published.Load() {
			e.published.Store(v.seq)
			r.Observer.RegistryEntries(v.layer, v.n)
		}
		e.busy.Store(false)
		// re-drain only if a newer value arrived while we were emitting
		if n := e.latest.Load(); n == nil || n.seq <= e.published.Load() {
			break
		}
	}
}

type leafEntry struct {
	Key        string     `json:"key"`
	Confidence Confidence `json:"confidence"`
	Expires    time.Time  `json:"expires"`
	Gen        uint64     `json:"gen"`
	lastUsed   atomic.Int64
}

type ladderEntry struct {
	Ladder   *Ladder   `json:"ladder"`
	Expires  time.Time `json:"expires"`
	Gen      uint64    `json:"gen"`
	lastUsed atomic.Int64
}

type targetEntry struct {
	Leaves      []string
	ExpansionID string
	Expires     time.Time
	lastUsed    atomic.Int64
}

type negEntry struct {
	Until    time.Time
	Failures int
	lastUsed atomic.Int64
}

// NewRegistry returns an empty registry. A non-nil store restores the
// generation counter and receives later writes.
func NewRegistry(o RegistryOptions, store Store) *Registry {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NegativeTTLMax < o.NegativeTTL {
		o.NegativeTTLMax = o.NegativeTTL
	}
	r := &Registry{
		leaves:   make(map[string]*leafEntry),
		ladders:  make(map[string]*ladderEntry),
		targets:  make(map[string]*targetEntry),
		negative: make(map[string]*negEntry),
		o:        o, now: o.Now,
	}
	r.SetStore(store)
	return r
}

// SetStore attaches a persistence store after construction, restoring the
// generation counter; called during startup, before anything has been learned.
func (r *Registry) SetStore(store Store) {
	if store == nil {
		return
	}
	r.store.Store(&store)
	if b, st, err := store.Retrieve(r.key("gen")); err == nil && st == status.LookupStatusHit {
		if g, err := strconv.ParseUint(string(b), 10, 64); err == nil {
			r.gen.Store(g)
		}
	}
}

func (r *Registry) getStore() Store {
	if p := r.store.Load(); p != nil {
		return *p
	}
	return nil
}

func (r *Registry) key(parts ...string) string {
	return r.o.KeyPrefix + "graphite.resolution." + strings.Join(parts, ".")
}

// Generation is the registry generation; entries recorded under an older
// generation are ignored
func (r *Registry) Generation() uint64 { return r.gen.Load() }

// BumpGeneration invalidates every learned entry, in memory and persisted;
// called on config reload and when a ladder is observed to have changed.
func (r *Registry) BumpGeneration() uint64 {
	g := r.gen.Add(1)
	r.mu.Lock()
	clear(r.leaves)
	clear(r.ladders)
	clear(r.targets)
	clear(r.negative)
	r.complete = 0
	var pends [4]pendingEmit
	for i, layer := range []string{LayerLeaf, LayerLadder, LayerTarget, LayerNegative} {
		pends[i] = r.captureEmit(layer, 0)
	}
	r.mu.Unlock()
	for _, p := range pends {
		r.publish(p)
	}
	if store := r.getStore(); store != nil {
		_ = store.Store(r.key("gen"), []byte(strconv.FormatUint(g, 10)), 0)
	}
	return g
}

// Leaf returns the ladder key and confidence bound to a leaf path
func (r *Registry) Leaf(path string) (string, Confidence, bool) {
	now := r.now()
	r.mu.RLock()
	e, ok := r.leaves[path]
	r.mu.RUnlock()
	if ok && e.Gen == r.gen.Load() && e.Expires.After(now) {
		e.lastUsed.Store(now.UnixNano())
		return e.Key, e.Confidence, true
	}
	store := r.getStore()
	if store == nil {
		return "", Unknown, false
	}
	// read-through
	b, st, err := store.Retrieve(r.key("leaf", path))
	if err != nil || st != status.LookupStatusHit {
		return "", Unknown, false
	}
	var pe leafEntry
	if json.Unmarshal(b, &pe) != nil || pe.Gen != r.gen.Load() || !pe.Expires.After(now) ||
		pe.Confidence == Unknown {
		return "", Unknown, false
	}
	pe.lastUsed.Store(now.UnixNano())
	r.mu.Lock()
	r.leaves[path] = &pe
	r.evictLocked(LayerLeaf)
	p := r.captureEmit(LayerLeaf, len(r.leaves))
	r.mu.Unlock()
	r.publish(p)
	return pe.Key, pe.Confidence, true
}

// SetLeaf binds a leaf path to a ladder key. The confidence must be Exact,
// Derived or Configured: recording Unknown is refused as speculative.
func (r *Registry) SetLeaf(path, ladderKey string, c Confidence) error {
	if c == Unknown || ladderKey == "" {
		return ErrSpeculative
	}
	now := r.now()
	e := &leafEntry{
		Key: ladderKey, Confidence: c, Expires: now.Add(r.o.TTL),
		Gen: r.gen.Load(),
	}
	e.lastUsed.Store(now.UnixNano())
	r.mu.Lock()
	r.leaves[path] = e
	delete(r.negative, path)
	r.evictLocked(LayerLeaf)
	p1 := r.captureEmit(LayerLeaf, len(r.leaves))
	p2 := r.captureEmit(LayerNegative, len(r.negative))
	r.mu.Unlock()
	r.publish(p1)
	r.publish(p2)
	if store := r.getStore(); store != nil {
		if b, err := json.Marshal(e); err == nil {
			_ = store.Store(r.key("leaf", path), b, r.o.TTL)
		}
	}
	return nil
}

// InvalidateLeaf removes a leaf's ladder binding; the store has no delete, so
// any persisted entry is overwritten with an already-expired one.
func (r *Registry) InvalidateLeaf(path string) {
	r.mu.Lock()
	delete(r.leaves, path)
	p := r.captureEmit(LayerLeaf, len(r.leaves))
	r.mu.Unlock()
	r.publish(p)
	if store := r.getStore(); store != nil {
		e := &leafEntry{
			Key: "-", Confidence: Exact, Expires: r.now().Add(-time.Second),
			Gen: r.gen.Load(),
		}
		if b, err := json.Marshal(e); err == nil {
			_ = store.Store(r.key("leaf", path), b, time.Minute)
		}
	}
}

// Ladder returns the ladder stored under a key
func (r *Registry) Ladder(key string) (*Ladder, bool) {
	now := r.now()
	r.mu.RLock()
	e, ok := r.ladders[key]
	r.mu.RUnlock()
	if ok && e.Gen == r.gen.Load() && e.Expires.After(now) {
		e.lastUsed.Store(now.UnixNano())
		return e.Ladder, true
	}
	store := r.getStore()
	if store == nil {
		return nil, false
	}
	b, st, err := store.Retrieve(r.key("ladder", key))
	if err != nil || st != status.LookupStatusHit {
		return nil, false
	}
	var pe ladderEntry
	if json.Unmarshal(b, &pe) != nil || pe.Ladder == nil || pe.Gen != r.gen.Load() ||
		!pe.Expires.After(now) {
		return nil, false
	}
	if pe.Ladder.State == StateComplete {
		l, err := NewLadder(pe.Ladder.Rungs)
		if err != nil {
			return nil, false
		}
		pe.Ladder = l
	}
	pe.lastUsed.Store(now.UnixNano())
	r.mu.Lock()
	r.storeLadderLocked(key, &pe)
	r.evictLocked(LayerLadder)
	p := r.captureEmit(LayerLadder, len(r.ladders))
	r.mu.Unlock()
	r.publish(p)
	return pe.Ladder, true
}

// inserts or replaces a ladder entry, keeping the complete-ladder count
// current; callers hold the write lock
func (r *Registry) storeLadderLocked(key string, e *ladderEntry) {
	if prev, ok := r.ladders[key]; ok {
		if prev.Ladder.State == StateComplete {
			r.complete--
		}
	}
	if e.Ladder.State == StateComplete {
		r.complete++
	}
	r.ladders[key] = e
}

// removes a ladder entry, keeping the complete-ladder count current; callers
// hold the write lock
func (r *Registry) deleteLadderLocked(key string) {
	if prev, ok := r.ladders[key]; ok {
		if prev.Ladder.State == StateComplete {
			r.complete--
		}
		delete(r.ladders, key)
	}
}

// SetLadder stores a ladder and returns its key: the fingerprint of a
// complete ladder, or "~"+leaf for the partial ladder of one leaf
func (r *Registry) SetLadder(leaf string, l *Ladder) (string, error) {
	if l == nil || l.State == StateUnknown {
		return "", ErrSpeculative
	}
	key := "~" + leaf
	if l.State == StateComplete {
		key = l.Fingerprint()
	}
	now := r.now()
	e := &ladderEntry{Ladder: l, Expires: now.Add(r.o.TTL), Gen: r.gen.Load()}
	e.lastUsed.Store(now.UnixNano())
	r.mu.Lock()
	r.storeLadderLocked(key, e)
	r.evictLocked(LayerLadder)
	p := r.captureEmit(LayerLadder, len(r.ladders))
	r.mu.Unlock()
	r.publish(p)
	if store := r.getStore(); store != nil && l.State == StateComplete {
		if b, err := json.Marshal(e); err == nil {
			_ = store.Store(r.key("ladder", key), b, r.o.TTL)
		}
	}
	return key, nil
}

// Target returns the cached expansion of a target path expression
func (r *Registry) Target(expr string) ([]string, string, bool) {
	now := r.now()
	r.mu.RLock()
	e, ok := r.targets[expr]
	r.mu.RUnlock()
	if !ok || !e.Expires.After(now) {
		return nil, "", false
	}
	e.lastUsed.Store(now.UnixNano())
	return e.Leaves, e.ExpansionID, true
}

// SetTarget caches an expansion and returns its token, a hash of the leaf
// set. It takes ownership of leaves, which must be sorted and deduplicated.
func (r *Registry) SetTarget(expr string, leaves []string, ttl time.Duration) string {
	now := r.now()
	e := &targetEntry{
		Leaves: leaves, ExpansionID: ExpansionID(leaves), Expires: now.Add(ttl),
	}
	e.lastUsed.Store(now.UnixNano())
	r.mu.Lock()
	r.targets[expr] = e
	r.evictLocked(LayerTarget)
	p := r.captureEmit(LayerTarget, len(r.targets))
	r.mu.Unlock()
	r.publish(p)
	return e.ExpansionID
}

// ExpansionID hashes a sorted leaf set
func ExpansionID(sortedLeaves []string) string {
	h := sha256.New()
	for _, l := range sortedLeaves {
		h.Write([]byte(l))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Negative reports whether a key is in backoff, and until when
func (r *Registry) Negative(key string) (time.Time, bool) {
	now := r.now()
	r.mu.RLock()
	e, ok := r.negative[key]
	r.mu.RUnlock()
	if !ok || !e.Until.After(now) {
		return time.Time{}, false
	}
	return e.Until, true
}

// SetNegative records a failed resolution with exponential backoff and
// returns the backoff applied
func (r *Registry) SetNegative(key string) time.Duration {
	now := r.now()
	r.mu.Lock()
	e, ok := r.negative[key]
	if !ok {
		e = &negEntry{}
		r.negative[key] = e
	}
	e.Failures++
	d := r.o.NegativeTTL
	for i := 1; i < e.Failures && d < r.o.NegativeTTLMax; i++ {
		d *= 2
	}
	d = min(d, r.o.NegativeTTLMax)
	e.Until = now.Add(d)
	e.lastUsed.Store(now.UnixNano())
	r.evictLocked(LayerNegative)
	p := r.captureEmit(LayerNegative, len(r.negative))
	r.mu.Unlock()
	r.publish(p)
	return d
}

// ClearNegative removes a key from backoff
func (r *Registry) ClearNegative(key string) {
	r.mu.Lock()
	delete(r.negative, key)
	p := r.captureEmit(LayerNegative, len(r.negative))
	r.mu.Unlock()
	r.publish(p)
}

// KnownLadders returns the complete ladders currently known, most-bound
// first; a new leaf is confirmed against these before discovery from scratch.
func (r *Registry) KnownLadders() []*Ladder {
	now := r.now()
	gen := r.gen.Load()
	r.mu.RLock()
	counts := make(map[string]int, len(r.ladders))
	for _, e := range r.leaves {
		if e.Gen == gen && e.Expires.After(now) {
			counts[e.Key]++
		}
	}
	type kl struct {
		l *Ladder
		n int
	}
	out := make([]kl, 0, len(r.ladders))
	for key, e := range r.ladders {
		if e.Ladder.State == StateComplete && e.Gen == gen && e.Expires.After(now) {
			out = append(out, kl{e.Ladder, counts[key]})
		}
	}
	r.mu.RUnlock()
	slices.SortStableFunc(out, func(a, b kl) int { return cmp.Compare(b.n, a.n) })
	ladders := make([]*Ladder, len(out))
	for i := range out {
		ladders[i] = out[i].l
	}
	return ladders
}

// Stats is the size of each layer
type Stats struct {
	Leaves, Ladders, Targets, Negatives int
	// CompleteLadders is the number of distinct complete ladders
	CompleteLadders int
}

// Stats returns the current layer sizes; it is called on every resolution
// lookup, so it must stay O(1) (the complete count is maintained on mutation).
func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Stats{
		Leaves: len(r.leaves), Ladders: len(r.ladders), Targets: len(r.targets),
		Negatives: len(r.negative), CompleteLadders: r.complete,
	}
}

// evictSampleSize is how many entries a sampled eviction examines per removal;
// map iteration's pseudo-random start makes a small sample approximate LRU.
const evictSampleSize = 8

// bounds a layer, with the write lock held, after it has grown: each removal
// samples a few entries and evicts an expired one, else the least recently used
func (r *Registry) evictLocked(layer string) {
	max := r.o.MaxEntries
	if max <= 0 {
		return
	}
	now := r.now()
	switch layer {
	case LayerLeaf:
		for len(r.leaves) > max {
			var victim string
			var oldest int64
			i := 0
			for k, e := range r.leaves {
				if !e.Expires.After(now) {
					victim = k
					break
				}
				if lu := e.lastUsed.Load(); victim == "" || lu < oldest {
					victim, oldest = k, lu
				}
				if i++; i >= evictSampleSize {
					break
				}
			}
			delete(r.leaves, victim)
		}
	case LayerLadder:
		for len(r.ladders) > max {
			var victim string
			var oldest int64
			i := 0
			for k, e := range r.ladders {
				if !e.Expires.After(now) {
					victim = k
					break
				}
				if lu := e.lastUsed.Load(); victim == "" || lu < oldest {
					victim, oldest = k, lu
				}
				if i++; i >= evictSampleSize {
					break
				}
			}
			r.deleteLadderLocked(victim)
		}
	case LayerTarget:
		for len(r.targets) > max {
			var victim string
			var oldest int64
			i := 0
			for k, e := range r.targets {
				if !e.Expires.After(now) {
					victim = k
					break
				}
				if lu := e.lastUsed.Load(); victim == "" || lu < oldest {
					victim, oldest = k, lu
				}
				if i++; i >= evictSampleSize {
					break
				}
			}
			delete(r.targets, victim)
		}
	case LayerNegative:
		for len(r.negative) > max {
			var victim string
			var oldest int64
			i := 0
			for k, e := range r.negative {
				if !e.Until.After(now) {
					victim = k
					break
				}
				if lu := e.lastUsed.Load(); victim == "" || lu < oldest {
					victim, oldest = k, lu
				}
				if i++; i >= evictSampleSize {
					break
				}
			}
			delete(r.negative, victim)
		}
	}
}
