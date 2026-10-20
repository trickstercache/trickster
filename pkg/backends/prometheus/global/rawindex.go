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

package global

import (
	"container/list"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/prometheus/model/labels"
)

const (
	// rawIndexLimit bounds the selectors a raw cache's index remembers, dropping the least recently used
	rawIndexLimit = 4096
	// rawIndexGroupLimit bounds the selectors remembered for one metric of one scope
	rawIndexGroupLimit = 64
	// rawSubsumeTries bounds the broader selectors one select looks to for the blocks it lacks
	rawSubsumeTries = 4
)

// rawKey is what a select's blocks are keyed by: its scope, the member and request identity, and its
// matchers in canonical order
type rawKey struct {
	key, scope string
	// metric is the selector's metric name when it has an equality matcher for one; only those are indexed
	metric   string
	matchers []*labels.Matcher
}

func (k rawKey) group() string { return k.scope + "\x00" + k.metric }

// rawIndex remembers the selectors whose blocks a cache has held, by scope and metric, so that a narrower
// selector can be answered from a broader one's blocks. It is only a hint: what it names may have expired.
type rawIndex struct {
	mu sync.Mutex
	// lru holds *rawIndexEntry, most recently used first
	lru    list.List
	groups map[string]map[string]*list.Element
}

type rawIndexEntry struct {
	group, key string
	matchers   []*labels.Matcher
	expires    time.Time
}

// rawCandidate is a broader selector's key, with the matchers its series must also meet
type rawCandidate struct {
	key   string
	extra []*labels.Matcher
}

// record notes that k's blocks are in the cache, until ttl after now
func (x *rawIndex) record(k rawKey, now time.Time, ttl time.Duration) {
	if k.metric == "" {
		return
	}
	g := k.group()
	x.mu.Lock()
	defer x.mu.Unlock()
	if el, ok := x.groups[g][k.key]; ok {
		el.Value.(*rawIndexEntry).expires = now.Add(ttl)
		x.lru.MoveToFront(el)
		return
	}
	if x.groups == nil {
		x.groups = make(map[string]map[string]*list.Element)
	}
	group := x.groups[g]
	if group == nil {
		group = make(map[string]*list.Element)
		x.groups[g] = group
	}
	if len(group) >= rawIndexGroupLimit {
		var oldest *list.Element
		for _, el := range group {
			if oldest == nil || el.Value.(*rawIndexEntry).expires.Before(oldest.Value.(*rawIndexEntry).expires) {
				oldest = el
			}
		}
		x.remove(oldest)
	}
	if x.lru.Len() >= rawIndexLimit {
		x.remove(x.lru.Back())
	}
	group[k.key] = x.lru.PushFront(&rawIndexEntry{
		group: g, key: k.key, matchers: k.matchers,
		expires: now.Add(ttl),
	})
}

// candidates returns the remembered selectors of k's scope and metric whose matchers are a strict subset of
// k's, narrowest first, as they hold the fewest series to filter
func (x *rawIndex) candidates(k rawKey, now time.Time) []rawCandidate {
	if k.metric == "" {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	group := x.groups[k.group()]
	if len(group) == 0 {
		return nil
	}
	var out []rawCandidate
	for _, el := range group {
		e := el.Value.(*rawIndexEntry)
		if now.After(e.expires) {
			x.remove(el)
			continue
		}
		if extra, ok := subsumes(e.matchers, k.matchers); ok {
			out = append(out, rawCandidate{key: e.key, extra: extra})
		}
	}
	slices.SortFunc(out, func(a, b rawCandidate) int { return len(a.extra) - len(b.extra) })
	return out[:min(len(out), rawSubsumeTries)]
}

func (x *rawIndex) remove(el *list.Element) {
	e := x.lru.Remove(el).(*rawIndexEntry)
	group := x.groups[e.group]
	delete(group, e.key)
	if len(group) == 0 {
		delete(x.groups, e.group)
	}
}

// subsumes reports whether broad, in canonical order, is a strict subset of narrow, by equal matchers, and
// returns the matchers of narrow it lacks
func subsumes(broad, narrow []*labels.Matcher) ([]*labels.Matcher, bool) {
	if len(broad) >= len(narrow) {
		return nil, false
	}
	extra := make([]*labels.Matcher, 0, len(narrow)-len(broad))
	i := 0
	for _, m := range narrow {
		if i < len(broad) && sameMatcher(broad[i], m) {
			i++
			continue
		}
		extra = append(extra, m)
	}
	return extra, i == len(broad)
}

func sameMatcher(a, b *labels.Matcher) bool {
	return a.Name == b.Name && a.Type == b.Type && a.Value == b.Value
}

// matchesAll reports whether lset meets every matcher, as a member selecting its series would
func matchesAll(lset labels.Labels, matchers []*labels.Matcher) bool {
	for _, m := range matchers {
		if !m.Matches(lset.Get(m.Name)) {
			return false
		}
	}
	return true
}
