/*
 * Copyright 2026 The Trickster Authors
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

package relay

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"

	"github.com/cespare/xxhash/v2"
)

const tableShards = 64

type tableShard struct {
	mu sync.Mutex
	m  map[string]*track
}

type table struct {
	shards [tableShards]tableShard
}

func (tb *table) init() {
	for i := range tb.shards {
		tb.shards[i].m = make(map[string]*track)
	}
}

func trackKey(b []byte, cacheNS string, scope Scope, name message.FullTrackName) []byte {
	// the relay's cache namespace, the scope and the full track name, each part length-prefixed so no two tracks
	// share a key; the key names the track's spilled groups, so relays sharing a cache never share them
	b = wire.AppendString(b, cacheNS)
	b = wire.AppendString(b, scope.Authority)
	b = wire.AppendString(b, scope.Path)
	b = wire.AppendNamespace(b, name.Namespace)
	return wire.AppendBytes(b, name.Name)
}

func (tb *table) acquire(r *Relay, scope Scope, name message.FullTrackName) *track {
	// the track is created when absent, and returned with a reference the caller releases
	var kb [256]byte
	key := trackKey(kb[:0], r.opts.CacheNamespace, scope, name)
	h := xxhash.Sum64(key)
	sh := &tb.shards[h%tableShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	t, ok := sh.m[string(key)]
	if !ok {
		t = newTrack(r, scope, name.Clone(), string(key), h)
		sh.m[t.key] = t
	}
	t.refs++
	return t
}

func (tb *table) release(t *track) {
	sh := &tb.shards[t.hash%tableShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	t.refs--
	tb.dropIfIdle(sh, t)
}

func (tb *table) dropIdle(t *track) {
	sh := &tb.shards[t.hash%tableShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	tb.dropIfIdle(sh, t)
}

func (tb *table) dropIfIdle(sh *tableShard, t *track) {
	// the shard lock is held, so no acquire can find the track while it is judged idle
	if t.refs > 0 || sh.m[t.key] != t || !t.idle() {
		return
	}
	delete(sh.m, t.key)
}

func (tb *table) each(fn func(t *track)) {
	for i := range tb.shards {
		sh := &tb.shards[i]
		sh.mu.Lock()
		ts := make([]*track, 0, len(sh.m))
		for _, t := range sh.m {
			ts = append(ts, t)
		}
		sh.mu.Unlock()
		for _, t := range ts {
			fn(t)
		}
	}
}
