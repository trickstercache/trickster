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
	"crypto/sha1"
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

// Store is the subset of cache.Cache the registry persists through
// (decision D6). A nil Store disables persistence.
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

// Registry is the three-layer resolution registry (decision D2):
//
//	leaf path  -> ladder key (fingerprint, or ~path for a partial ladder)
//	ladder key -> *Ladder
//	target     -> expanded leaf set + expansion token
//
// plus a negative cache of keys that recently failed to resolve. Reads are
// lock-free-ish (RWMutex read side) and the lookup path never blocks on I/O
// or channels; persistence is a write-through and a read-through on miss.
type Registry struct {
	mu       sync.RWMutex
	leaves   map[string]*leafEntry
	ladders  map[string]*ladderEntry
	targets  map[string]*targetEntry
	negative map[string]*negEntry
	gen      atomic.Uint64
	o        RegistryOptions
	store    Store
	now      func() time.Time
}

type leafEntry struct {
	Key        string     `json:"key"`
	Confidence Confidence `json:"confidence"`
	Expires    time.Time  `json:"expires"`
	Gen        uint64     `json:"gen"`
	lastUsed   int64
}

type ladderEntry struct {
	Ladder   *Ladder   `json:"ladder"`
	Expires  time.Time `json:"expires"`
	Gen      uint64    `json:"gen"`
	lastUsed int64
}

type targetEntry struct {
	Leaves      []string
	ExpansionID string
	Expires     time.Time
	lastUsed    int64
}

type negEntry struct {
	Until    time.Time
	Failures int
	lastUsed int64
}

// NewRegistry returns an empty registry. When store is non-nil, the
// generation counter is restored from it so that entries persisted before a
// restart remain valid, and later writes go through to it.
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
		o:        o, store: store, now: o.Now,
	}
	if store != nil {
		if b, st, err := store.Retrieve(r.key("gen")); err == nil && st == status.LookupStatusHit {
			if g, err := strconv.ParseUint(string(b), 10, 64); err == nil {
				r.gen.Store(g)
			}
		}
	}
	return r
}

func (r *Registry) key(parts ...string) string {
	return r.o.KeyPrefix + "graphite.resolution." + strings.Join(parts, ".")
}

// Generation is the registry generation; entries recorded under an older
// generation are ignored
func (r *Registry) Generation() uint64 { return r.gen.Load() }

// BumpGeneration invalidates every learned entry, in memory and persisted.
// Call it on config reload and whenever a ladder is observed to have
// changed (a step misprediction).
func (r *Registry) BumpGeneration() uint64 {
	g := r.gen.Add(1)
	r.mu.Lock()
	clear(r.leaves)
	clear(r.ladders)
	clear(r.targets)
	clear(r.negative)
	r.mu.Unlock()
	if r.store != nil {
		_ = r.store.Store(r.key("gen"), []byte(strconv.FormatUint(g, 10)), 0)
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
		atomic.StoreInt64(&e.lastUsed, now.UnixNano())
		return e.Key, e.Confidence, true
	}
	if r.store == nil {
		return "", Unknown, false
	}
	// read-through
	b, st, err := r.store.Retrieve(r.key("leaf", path))
	if err != nil || st != status.LookupStatusHit {
		return "", Unknown, false
	}
	var pe leafEntry
	if json.Unmarshal(b, &pe) != nil || pe.Gen != r.gen.Load() || !pe.Expires.After(now) ||
		pe.Confidence == Unknown {
		return "", Unknown, false
	}
	pe.lastUsed = now.UnixNano()
	r.mu.Lock()
	r.leaves[path] = &pe
	r.evictLocked(LayerLeaf)
	r.mu.Unlock()
	return pe.Key, pe.Confidence, true
}

// SetLeaf binds a leaf path to a ladder key. The confidence must be Exact,
// Derived or Configured: recording Unknown is the speculative write the
// design forbids (§4.5), and is refused.
func (r *Registry) SetLeaf(path, ladderKey string, c Confidence) error {
	if c == Unknown || ladderKey == "" {
		return ErrSpeculative
	}
	now := r.now()
	e := &leafEntry{
		Key: ladderKey, Confidence: c, Expires: now.Add(r.o.TTL),
		Gen: r.gen.Load(), lastUsed: now.UnixNano(),
	}
	r.mu.Lock()
	r.leaves[path] = e
	delete(r.negative, path)
	r.evictLocked(LayerLeaf)
	r.mu.Unlock()
	if r.store != nil {
		if b, err := json.Marshal(e); err == nil {
			_ = r.store.Store(r.key("leaf", path), b, r.o.TTL)
		}
	}
	return nil
}

// Ladder returns the ladder stored under a key
func (r *Registry) Ladder(key string) (*Ladder, bool) {
	now := r.now()
	r.mu.RLock()
	e, ok := r.ladders[key]
	r.mu.RUnlock()
	if ok && e.Gen == r.gen.Load() && e.Expires.After(now) {
		atomic.StoreInt64(&e.lastUsed, now.UnixNano())
		return e.Ladder, true
	}
	if r.store == nil {
		return nil, false
	}
	b, st, err := r.store.Retrieve(r.key("ladder", key))
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
	pe.lastUsed = now.UnixNano()
	r.mu.Lock()
	r.ladders[key] = &pe
	r.evictLocked(LayerLadder)
	r.mu.Unlock()
	return pe.Ladder, true
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
	e := &ladderEntry{Ladder: l, Expires: now.Add(r.o.TTL), Gen: r.gen.Load(), lastUsed: now.UnixNano()}
	r.mu.Lock()
	r.ladders[key] = e
	r.evictLocked(LayerLadder)
	r.mu.Unlock()
	if r.store != nil && l.State == StateComplete {
		if b, err := json.Marshal(e); err == nil {
			_ = r.store.Store(r.key("ladder", key), b, r.o.TTL)
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
	atomic.StoreInt64(&e.lastUsed, now.UnixNano())
	return e.Leaves, e.ExpansionID, true
}

// SetTarget caches an expansion and returns its expansion token: a hash of
// the sorted leaf set, so that a new leaf under a wildcard changes the
// token and therefore the cache key (design note §5.5)
func (r *Registry) SetTarget(expr string, leaves []string, ttl time.Duration) string {
	now := r.now()
	sorted := slices.Clone(leaves)
	slices.Sort(sorted)
	e := &targetEntry{
		Leaves: sorted, ExpansionID: ExpansionID(sorted), Expires: now.Add(ttl),
		lastUsed: now.UnixNano(),
	}
	r.mu.Lock()
	r.targets[expr] = e
	r.evictLocked(LayerTarget)
	r.mu.Unlock()
	return e.ExpansionID
}

// ExpansionID hashes a sorted leaf set
func ExpansionID(sortedLeaves []string) string {
	h := sha1.New()
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
	e.lastUsed = now.UnixNano()
	r.evictLocked(LayerNegative)
	r.mu.Unlock()
	return d
}

// ClearNegative removes a key from backoff
func (r *Registry) ClearNegative(key string) {
	r.mu.Lock()
	delete(r.negative, key)
	r.mu.Unlock()
}

// Stats is the size of each layer
type Stats struct {
	Leaves, Ladders, Targets, Negatives int
	// CompleteLadders is the number of distinct complete ladders
	CompleteLadders int
}

// Stats returns the current layer sizes
func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Stats{
		Leaves: len(r.leaves), Ladders: len(r.ladders), Targets: len(r.targets),
		Negatives: len(r.negative),
	}
	for _, e := range r.ladders {
		if e.Ladder.State == StateComplete {
			s.CompleteLadders++
		}
	}
	return s
}

// evictLocked bounds a layer: expired entries go first, then the least
// recently used tenth. Called with the write lock held, and only when a
// layer has just grown, so the sort is rare.
func (r *Registry) evictLocked(layer string) {
	max := r.o.MaxEntries
	if max <= 0 {
		return
	}
	now := r.now()
	type item struct {
		key      string
		lastUsed int64
	}
	var items []item
	switch layer {
	case LayerLeaf:
		if len(r.leaves) <= max {
			return
		}
		for k, e := range r.leaves {
			if !e.Expires.After(now) {
				delete(r.leaves, k)
				continue
			}
			items = append(items, item{k, e.lastUsed})
		}
	case LayerLadder:
		if len(r.ladders) <= max {
			return
		}
		for k, e := range r.ladders {
			if !e.Expires.After(now) {
				delete(r.ladders, k)
				continue
			}
			items = append(items, item{k, e.lastUsed})
		}
	case LayerTarget:
		if len(r.targets) <= max {
			return
		}
		for k, e := range r.targets {
			if !e.Expires.After(now) {
				delete(r.targets, k)
				continue
			}
			items = append(items, item{k, e.lastUsed})
		}
	case LayerNegative:
		if len(r.negative) <= max {
			return
		}
		for k, e := range r.negative {
			if !e.Until.After(now) {
				delete(r.negative, k)
				continue
			}
			items = append(items, item{k, e.lastUsed})
		}
	}
	if len(items) <= max {
		return
	}
	slices.SortFunc(items, func(a, b item) int { return cmp.Compare(a.lastUsed, b.lastUsed) })
	n := len(items) - max + max/10
	for _, it := range items[:n] {
		switch layer {
		case LayerLeaf:
			delete(r.leaves, it.key)
		case LayerLadder:
			delete(r.ladders, it.key)
		case LayerTarget:
			delete(r.targets, it.key)
		case LayerNegative:
			delete(r.negative, it.key)
		}
	}
}
