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

package graphite

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tzCacheCounts(c *tzCache) (int, int) {
	pos := 0
	c.pos.Range(func(_, _ any) bool { pos++; return true })
	c.mu.Lock()
	defer c.mu.Unlock()
	neg := 0
	if c.negLL != nil {
		neg = c.negLL.Len()
	}
	return pos, neg
}

func TestTZCachePositivesImmuneToNegativeChurn(t *testing.T) {
	now := time.Now()
	var loads atomic.Int64
	hot, _ := time.LoadLocation("America/New_York")
	c := &tzCache{
		now: func() time.Time { return now },
		loader: func(name string) (*time.Location, error) {
			loads.Add(1)
			if strings.HasPrefix(name, "Not/") {
				return nil, fmt.Errorf("unknown zone %s", name)
			}
			return hot, nil
		},
	}
	if loc, res := c.get("America/New_York"); res != tzValid || loc != hot {
		t.Fatal("hot zone did not load")
	}

	// the exact repeating capacity+1 hostile cycle, three times over: the
	// cold-load budget bounds loader invocations regardless of cache churn
	cycle := make([]string, tzNegMax+1)
	for i := range cycle {
		cycle[i] = fmt.Sprintf("Not/AZone%d", i)
	}
	before := loads.Load()
	for range 3 {
		for _, name := range cycle {
			if loc, res := c.get(name); res == tzValid || loc != nil {
				t.Fatal("a hostile name must never resolve to a location")
			}
		}
	}
	if spent := loads.Load() - before; spent > tzLoadBurst {
		t.Fatalf("771 hostile lookups performed %d loads; the budget is %d", spent, tzLoadBurst)
	}
	// the positive entry is untouched throughout
	if loc, res := c.get("America/New_York"); res != tzValid || loc != hot {
		t.Fatal("negative churn evicted a positive entry")
	}
	pos, neg := tzCacheCounts(c)
	if pos != 1 || neg > tzNegMax {
		t.Errorf("counts: pos=%d neg=%d", pos, neg)
	}

	// the budget refills with time: legitimate new zones load again
	now = now.Add(time.Minute)
	if loc, res := c.get("Europe/London"); res != tzValid || loc == nil {
		t.Fatal("a valid zone must load once the budget refills")
	}

	// over-length names are refused without any load
	before = loads.Load()
	long := make([]byte, maxTZLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, res := c.get(string(long)); res != tzInvalid {
		t.Fatal("over-length name must be refused as definitively invalid")
	}
	if loads.Load() != before {
		t.Fatal("an over-length name must not reach the loader")
	}
}

func TestTZCacheConcurrentDistinctMissesBounded(t *testing.T) {
	now := time.Now()
	var loads atomic.Int64
	c := &tzCache{
		now: func() time.Time { return now },
		loader: func(name string) (*time.Location, error) {
			loads.Add(1)
			return nil, fmt.Errorf("unknown zone %s", name)
		},
	}
	var wg sync.WaitGroup
	for g := range 200 {
		wg.Go(func() {
			c.get(fmt.Sprintf("Not/Distinct%d", g))
		})
	}
	wg.Wait()
	if n := loads.Load(); n > tzLoadBurst {
		t.Fatalf("200 concurrent distinct misses performed %d loads; the budget is %d", n, tzLoadBurst)
	}
}

func TestTZCacheConcurrentColdStart(t *testing.T) {
	c := &tzCache{}
	hot, _ := c.get("America/New_York")
	if hot == nil {
		t.Fatal("hot zone did not load")
	}

	// hold the cold-load path hostage: acquire the cache mutex, then show
	// positive hits still complete (they never take it)
	c.mu.Lock()
	done := make(chan *time.Location, 1)
	go func() {
		loc, _ := c.get("America/New_York")
		done <- loc
	}()
	select {
	case loc := <-done:
		if loc != hot {
			t.Fatal("hit returned a different location")
		}
	case <-time.After(2 * time.Second):
		c.mu.Unlock()
		t.Fatal("a positive hit blocked on the cold-load mutex")
	}
	c.mu.Unlock()

	// concurrent cold lookups for one name coalesce
	var wg sync.WaitGroup
	results := make([]*time.Location, 32)
	for g := range 32 {
		wg.Go(func() {
			loc, res := c.get("Australia/Sydney")
			if res != tzValid {
				t.Error("unexpected refusal")
				return
			}
			results[g] = loc
		})
	}
	wg.Wait()
	for g, loc := range results {
		if loc == nil || loc != results[0] {
			t.Fatalf("goroutine %d got a different location", g)
		}
	}
	pos, _ := tzCacheCounts(c)
	if pos != 2 { // America/New_York and Australia/Sydney
		t.Errorf("expected 2 positives, got %d", pos)
	}
}

func TestTZCacheExhaustionIsUnavailableNotInvalid(t *testing.T) {
	now := time.Now()
	var loads atomic.Int64
	hot, _ := time.LoadLocation("America/New_York")
	c := &tzCache{
		now: func() time.Time { return now },
		loader: func(name string) (*time.Location, error) {
			loads.Add(1)
			if strings.HasPrefix(name, "Not/") {
				return nil, fmt.Errorf("unknown zone %s", name)
			}
			return hot, nil
		},
	}
	// hostile traffic spends the whole budget on distinct invalid names
	for i := range tzLoadBurst + 8 {
		c.get(fmt.Sprintf("Not/Hostile%d", i))
	}
	before := loads.Load()
	// the next legitimate first lookup of a valid zone is unavailable — not
	// invalid — and performs no load
	loc, res := c.get("America/New_York")
	if res != tzUnavailable || loc != nil {
		t.Fatalf("expected tzUnavailable for a cold zone with no budget, got %v %v", loc, res)
	}
	if loads.Load() != before {
		t.Fatal("an unavailable lookup must not reach the loader")
	}
	// nothing was cached for the undetermined name
	if pos, neg := tzCacheCounts(c); pos != 0 || neg != tzLoadBurst {
		t.Fatalf("unexpected cache contents after exhaustion: pos=%d neg=%d", pos, neg)
	}
	// once the budget refills, the same zone loads and is valid
	now = now.Add(time.Minute)
	if loc, res := c.get("America/New_York"); res != tzValid || loc != hot {
		t.Fatal("a valid zone must load once the budget refills")
	}
}
