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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

// fakeStore is an in-memory Store
type fakeStore struct {
	mu   sync.Mutex
	data map[string][]byte
	fail bool
}

func newFakeStore() *fakeStore { return &fakeStore{data: make(map[string][]byte)} }

func (f *fakeStore) Store(key string, data []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("store failed")
	}
	f.data[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeStore) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, status.LookupStatusError, errors.New("retrieve failed")
	}
	b, ok := f.data[key]
	if !ok {
		return nil, status.LookupStatusKeyMiss, nil
	}
	return b, status.LookupStatusHit, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestRegistry(store Store) (*Registry, *clock) {
	c := &clock{t: time.Unix(1_787_000_000, 0)}
	r := NewRegistry(RegistryOptions{TTL: time.Hour, NegativeTTL: 10 * time.Second,
		NegativeTTLMax: time.Minute, MaxEntries: 4, KeyPrefix: "b1.", Now: c.now}, store)
	return r, c
}

func TestRegistrySpeculativeWritesRefused(t *testing.T) {
	r, _ := newTestRegistry(nil)
	if err := r.SetLeaf("a.b", "fp", Unknown); !errors.Is(err, ErrSpeculative) {
		t.Errorf("expected ErrSpeculative, got %v", err)
	}
	if err := r.SetLeaf("a.b", "", Exact); !errors.Is(err, ErrSpeculative) {
		t.Errorf("expected ErrSpeculative for an empty ladder key, got %v", err)
	}
	if _, err := r.SetLadder("a.b", nil); !errors.Is(err, ErrSpeculative) {
		t.Errorf("expected ErrSpeculative for a nil ladder, got %v", err)
	}
	if _, err := r.SetLadder("a.b", &Ladder{}); !errors.Is(err, ErrSpeculative) {
		t.Errorf("expected ErrSpeculative for an unknown-state ladder, got %v", err)
	}
	if _, _, ok := r.Leaf("a.b"); ok {
		t.Error("nothing must have been recorded")
	}
}

func TestRegistryLeavesAndLadders(t *testing.T) {
	store := newFakeStore()
	r, c := newTestRegistry(store)
	l, _ := ParseRetentions("10s:6h,60s:7d")
	key, err := r.SetLadder("a.b", l)
	if err != nil || key != l.Fingerprint() {
		t.Fatalf("unexpected key %q err %v", key, err)
	}
	if err := r.SetLeaf("a.b", key, Exact); err != nil {
		t.Fatal(err)
	}
	if k, conf, ok := r.Leaf("a.b"); !ok || k != key || conf != Exact {
		t.Errorf("leaf lookup: %q %v %t", k, conf, ok)
	}
	if got, ok := r.Ladder(key); !ok || got.String() != l.String() {
		t.Errorf("ladder lookup: %v %t", got, ok)
	}
	// a partial ladder is keyed per leaf and not persisted
	p := NewPartial()
	_ = p.Observe(time.Hour, 10*time.Second)
	pk, err := r.SetLadder("c.d", p)
	if err != nil || pk != "~c.d" {
		t.Fatalf("partial key %q err %v", pk, err)
	}
	if _, ok := store.data["b1.graphite.resolution.ladder.~c.d"]; ok {
		t.Error("partial ladders must not be persisted")
	}
	st := r.Stats()
	if st.Leaves != 1 || st.Ladders != 2 || st.CompleteLadders != 1 {
		t.Errorf("unexpected stats %+v", st)
	}

	// persistence: a fresh registry over the same store reads through
	r2, _ := newTestRegistry(store)
	r2.now = c.now
	if k, conf, ok := r2.Leaf("a.b"); !ok || k != key || conf != Exact {
		t.Errorf("read-through leaf: %q %v %t", k, conf, ok)
	}
	if got, ok := r2.Ladder(key); !ok || got.String() != l.String() || got.Fingerprint() != key {
		t.Errorf("read-through ladder: %v %t", got, ok)
	}
	// expiry
	c.advance(2 * time.Hour)
	if _, _, ok := r.Leaf("a.b"); ok {
		t.Error("expired leaf must miss")
	}
	if _, ok := r.Ladder(key); ok {
		t.Error("expired ladder must miss")
	}
	c.advance(-2 * time.Hour)

	// generation bump invalidates memory and persisted entries
	g := r.BumpGeneration()
	if r.Generation() != g || g != 1 {
		t.Errorf("generation %d", g)
	}
	if _, _, ok := r.Leaf("a.b"); ok {
		t.Error("bumped leaf must miss")
	}
	r3, _ := newTestRegistry(store)
	if r3.Generation() != 1 {
		t.Errorf("generation must be restored from the store, got %d", r3.Generation())
	}
	if _, _, ok := r3.Leaf("a.b"); ok {
		t.Error("persisted entry from an older generation must be ignored")
	}
	if _, ok := r3.Ladder(key); ok {
		t.Error("persisted ladder from an older generation must be ignored")
	}
	// corrupt persisted data is ignored
	store.data["b1.graphite.resolution.leaf.z"] = []byte("not json")
	store.data["b1.graphite.resolution.ladder.z"] = []byte("not json")
	if _, _, ok := r3.Leaf("z"); ok {
		t.Error("corrupt leaf")
	}
	if _, ok := r3.Ladder("z"); ok {
		t.Error("corrupt ladder")
	}
	// a persisted complete ladder that fails validation is ignored
	store.data["b1.graphite.resolution.ladder.bad"] = []byte(
		`{"ladder":{"rungs":[{"max_age":1,"step":2}],"state":2},"expires":"2100-01-01T00:00:00Z","gen":1}`)
	if _, ok := r3.Ladder("bad"); ok {
		t.Error("invalid persisted ladder must be ignored")
	}
	// a failing store is tolerated
	store.fail = true
	if _, _, ok := r3.Leaf("a.b"); ok {
		t.Error("store failure")
	}
	if err := r3.SetLeaf("q", "fp", Exact); err != nil {
		t.Error("store failures must not fail the in-memory write")
	}
	store.fail = false
}

func TestRegistryTargetsAndNegative(t *testing.T) {
	r, c := newTestRegistry(nil)
	id := r.SetTarget("a.*", []string{"a.b", "a.a"}, time.Minute)
	leaves, id2, ok := r.Target("a.*")
	if !ok || id != id2 || strings.Join(leaves, ",") != "a.a,a.b" {
		t.Errorf("target: %v %q %t", leaves, id2, ok)
	}
	if ExpansionID([]string{"a.a", "a.b"}) != id || ExpansionID([]string{"a.a", "a.b", "a.c"}) == id {
		t.Error("expansion id must depend on the leaf set")
	}
	c.advance(2 * time.Minute)
	if _, _, ok := r.Target("a.*"); ok {
		t.Error("expired target must miss")
	}

	// negative cache with backoff
	if _, ok := r.Negative("x"); ok {
		t.Error("not negative yet")
	}
	if d := r.SetNegative("x"); d != 10*time.Second {
		t.Errorf("first backoff %v", d)
	}
	if d := r.SetNegative("x"); d != 20*time.Second {
		t.Errorf("second backoff %v", d)
	}
	for range 5 {
		r.SetNegative("x")
	}
	if d := r.SetNegative("x"); d != time.Minute {
		t.Errorf("backoff must cap at NegativeTTLMax, got %v", d)
	}
	until, ok := r.Negative("x")
	if !ok || until.Sub(c.now()) != time.Minute {
		t.Errorf("negative until %v %t", until, ok)
	}
	c.advance(2 * time.Minute)
	if _, ok := r.Negative("x"); ok {
		t.Error("negative entry must expire")
	}
	r.SetNegative("y")
	r.ClearNegative("y")
	if _, ok := r.Negative("y"); ok {
		t.Error("cleared")
	}
	// a successful leaf write clears the negative entry
	r.SetNegative("z")
	_ = r.SetLeaf("z", "fp", Exact)
	if _, ok := r.Negative("z"); ok {
		t.Error("SetLeaf must clear negative")
	}
}

func TestRegistryEviction(t *testing.T) {
	r, c := newTestRegistry(nil)
	l, _ := ParseRetentions("10s:6h")
	for i := range 10 {
		_ = r.SetLeaf(fmt.Sprintf("leaf%d", i), l.Fingerprint(), Exact)
		c.advance(time.Second)
	}
	if st := r.Stats(); st.Leaves > 4 {
		t.Errorf("leaf layer not bounded: %d", st.Leaves)
	}
	// the most recently used survive
	if _, _, ok := r.Leaf("leaf9"); !ok {
		t.Error("most recent leaf evicted")
	}
	if _, _, ok := r.Leaf("leaf0"); ok {
		t.Error("least recent leaf retained")
	}
	for i := range 10 {
		ll, _ := ParseRetentions(fmt.Sprintf("%ds:6h", 10+i*10))
		_, _ = r.SetLadder("x", ll)
		r.SetTarget(fmt.Sprintf("t%d.*", i), []string{"a"}, time.Hour)
		r.SetNegative(fmt.Sprintf("n%d", i))
		c.advance(time.Second)
	}
	st := r.Stats()
	if st.Ladders > 4 || st.Targets > 4 || st.Negatives > 4 {
		t.Errorf("layers not bounded: %+v", st)
	}
	// expired entries are evicted first
	r2, c2 := newTestRegistry(nil)
	_ = r2.SetLeaf("old", "fp", Exact)
	c2.advance(2 * time.Hour)
	for i := range 4 {
		_ = r2.SetLeaf(fmt.Sprintf("new%d", i), "fp", Exact)
	}
	if _, _, ok := r2.Leaf("old"); ok {
		t.Error("expired entry must be evicted first")
	}
	if st := r2.Stats(); st.Leaves != 4 {
		t.Errorf("expected the 4 fresh leaves, got %d", st.Leaves)
	}
	// unbounded registry never evicts
	r3 := NewRegistry(RegistryOptions{TTL: time.Hour, NegativeTTL: time.Second}, nil)
	for i := range 50 {
		_ = r3.SetLeaf(fmt.Sprintf("l%d", i), "fp", Exact)
	}
	if r3.Stats().Leaves != 50 {
		t.Error("unbounded registry evicted")
	}
}
