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
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	spillFormat      = 1
	spillIndexMax    = 4096
	cacheKeyPrefix   = "moq:"
	objectOverhead   = 96
	flagToEnd        = 0x01
	objFlagDatagram  = 0x01
	objFlagDefault   = 0x02
	objFlagEndOfGrp  = 0x04
	maxCachedObjects = 1 << 20
)

var errSpillFormat = errors.New("moq: cached group is malformed")

type cachedGroup struct {
	key     string
	group   uint64
	from    uint64
	to      uint64
	toEnd   bool
	objects []*object
}

func (cg *cachedGroup) Size() int {
	n := len(cg.key) + objectOverhead
	for _, o := range cg.objects {
		n += int(o.size()) + objectOverhead
	}
	return n
}

func (cg *cachedGroup) covers(lo, hi uint64) bool {
	// the objects cover [from, to], or from to the group's end; a memory cache keeps the group by reference
	return cg.from <= lo && (cg.toEnd || (hi != math.MaxUint64 && hi <= cg.to))
}

func (cg *cachedGroup) encode() []byte {
	n := 32 + len(cg.key)
	for _, o := range cg.objects {
		n += 32 + len(o.props) + len(o.payload)
	}
	b := make([]byte, 0, n)
	b = append(b, spillFormat)
	b = wire.AppendString(b, cg.key)
	b = wire.AppendVarint(b, cg.group)
	b = wire.AppendVarint(b, cg.from)
	b = wire.AppendVarint(b, cg.to)
	var flags byte
	if cg.toEnd {
		flags |= flagToEnd
	}
	b = append(b, flags)
	b = wire.AppendVarint(b, uint64(len(cg.objects)))
	for _, o := range cg.objects {
		var of byte
		if o.datagram {
			of |= objFlagDatagram
		}
		if o.defaultPrio {
			of |= objFlagDefault
		}
		if o.endOfGroup {
			of |= objFlagEndOfGrp
		}
		b = append(b, of, o.priority)
		b = wire.AppendVarint(b, o.id)
		b = wire.AppendVarint(b, o.subgroup)
		b = wire.AppendVarint(b, uint64(o.status))
		b = wire.AppendVarint(b, uint64(o.arrived.UnixMilli()))
		b = wire.AppendBytes(b, o.props)
		b = wire.AppendBytes(b, o.payload)
	}
	return b
}

func decodeGroup(b []byte, key string) (*cachedGroup, error) {
	r := wire.NewReader(b)
	if r.Uint8() != spillFormat {
		return nil, errSpillFormat
	}
	if string(r.LenBytes(uint64(len(b)))) != key {
		// another track whose key hashed alike; never serve it as this one
		return nil, errSpillFormat
	}
	cg := &cachedGroup{key: key, group: r.Varint(), from: r.Varint(), to: r.Varint()}
	cg.toEnd = r.Uint8()&flagToEnd != 0
	n := r.Count()
	if r.Err() != nil || n > maxCachedObjects {
		return nil, errSpillFormat
	}
	cg.objects = make([]*object, 0, n)
	for range n {
		of, prio := r.Uint8(), r.Uint8()
		o := &object{
			group: cg.group, id: r.Varint(), subgroup: r.Varint(), priority: prio,
			datagram: of&objFlagDatagram != 0, defaultPrio: of&objFlagDefault != 0, endOfGroup: of&objFlagEndOfGrp != 0,
		}
		o.status = message.ObjectStatus(r.Varint())
		o.arrived = time.UnixMilli(int64(min(r.Varint(), math.MaxInt64))) // #nosec G115 -- clamped above
		o.props = message.Properties(r.LenBytes(uint64(len(b))))
		o.payload = r.LenBytes(uint64(len(b)))
		o.filled.Store(int64(len(o.payload)))
		cg.objects = append(cg.objects, o)
	}
	if err := r.Done(); err != nil {
		return nil, errSpillFormat
	}
	return cg, nil
}

type spillJob struct {
	t   *track
	cg  *cachedGroup
	ttl time.Duration
}

func (j spillJob) run(r *Relay) {
	c := r.cache()
	if c == nil {
		return
	}
	key := j.t.cacheKey(j.cg.group)
	// the index names the group before it lands, so a fetch never misses a stored group; a miss costs a lookup
	j.t.recordSpill(j.cg.group, true)
	var err error
	if mc, ok := byReference(c); ok {
		err = mc.StoreReference(key, j.cg, j.ttl)
	} else {
		err = c.Store(key, j.cg.encode(), j.ttl)
	}
	if err != nil {
		j.t.recordSpill(j.cg.group, false)
		r.m.fail(errClassCache)
	}
}

func byReference(c cache.Cache) (cache.MemoryCache, bool) {
	// every cache.Cache offers references, but only a memory cache keeps them; the others refuse at the call
	if cfg := c.Configuration(); cfg == nil || cfg.Provider != cacheproviders.Memory {
		return nil, false
	}
	mc, ok := c.(cache.MemoryCache)
	return mc, ok
}

func (t *track) cacheKey(group uint64) string {
	b := make([]byte, 0, len(cacheKeyPrefix)+34)
	b = append(b, cacheKeyPrefix...)
	b = strconv.AppendUint(b, t.hash, 16)
	b = append(b, ':')
	b = strconv.AppendUint(b, group, 10)
	return string(b)
}

func (t *track) spillJob(cg *cachedGroup) (spillJob, bool) {
	// a group already past MAX_CACHE_DURATION is not cached at all
	ttl := t.r.opts.MaxTTL
	if mcd, ok := t.properties().MaxCacheDuration(); ok {
		oldest := time.Now()
		for _, o := range cg.objects {
			if o.arrived.Before(oldest) {
				oldest = o.arrived
			}
		}
		left := millis(mcd) - time.Since(oldest)
		if left <= 0 {
			return spillJob{}, false
		}
		ttl = min(ttl, left)
	}
	return spillJob{t: t, cg: cg, ttl: ttl}, true
}

func (t *track) spillJobLocked(g *group) (spillJob, bool) {
	// the caller holds t.mu; only complete objects of a covered stretch are kept, and an empty group not at all
	objs := g.sorted()
	kept := objs[:0:0]
	for _, o := range objs {
		if o.complete() {
			kept = append(kept, o)
		}
	}
	cg := &cachedGroup{key: t.key, group: g.id}
	if len(kept) > 0 {
		first := kept[0]
		cg.from = first.id - min(first.priorObjectGap(), first.id)
		expect := cg.from
		for _, o := range kept {
			if o.id-min(o.priorObjectGap(), o.id) > expect {
				break
			}
			expect = o.id + 1
		}
		cg.to = expect - 1
		cg.toEnd = g.hasEnd && cg.to >= g.end
		for i, o := range kept {
			if o.id > cg.to {
				kept = kept[:i]
				break
			}
		}
	}
	cg.objects = kept
	if len(kept) == 0 {
		return spillJob{}, false
	}
	ttl := t.r.opts.MaxTTL
	if mcd, ok := t.props.MaxCacheDuration(); ok {
		left := millis(mcd) - time.Since(g.created)
		if left <= 0 {
			return spillJob{}, false
		}
		ttl = min(ttl, left)
	}
	return spillJob{t: t, cg: cg, ttl: ttl}, true
}

func (t *track) recordSpill(group uint64, stored bool) {
	t.mu.Lock()
	if stored {
		t.spilled.add(group)
	} else {
		t.spilled.remove(group)
	}
	t.mu.Unlock()
}

func (t *track) cachedGroup(group uint64) *cachedGroup {
	c := t.r.cache()
	if c == nil {
		return nil
	}
	t.mu.Lock()
	known := t.spilled.has(group)
	t.mu.Unlock()
	if !known {
		return nil
	}
	key := t.cacheKey(group)
	if mc, ok := byReference(c); ok {
		v, _, err := mc.RetrieveReference(key)
		if err != nil {
			return nil
		}
		if cg, ok := v.(*cachedGroup); ok && cg.key == t.key {
			return t.unexpired(cg)
		}
		return nil
	}
	b, _, err := c.Retrieve(key)
	if err != nil {
		return nil
	}
	cg, err := decodeGroup(b, t.key)
	if err != nil {
		return nil
	}
	return t.unexpired(cg)
}

func (t *track) unexpired(cg *cachedGroup) *cachedGroup {
	// an object past MAX_CACHE_DURATION is unknown again, so a group holding one is not served whole
	mcd, ok := t.properties().MaxCacheDuration()
	if !ok {
		return cg
	}
	limit := millis(mcd)
	for _, o := range cg.objects {
		if time.Since(o.arrived) > limit {
			return nil
		}
	}
	return cg
}

func (t *track) liveGroup(id, lo, hi uint64) ([]*object, bool) {
	t.mu.Lock()
	g := t.groups[id]
	if g == nil {
		t.mu.Unlock()
		return nil, false
	}
	objs, hasEnd, end := g.sorted(), g.hasEnd, g.end
	mcd, limited := t.props.MaxCacheDuration()
	t.mu.Unlock()
	if limited {
		limit := millis(mcd)
		fresh := objs[:0:0]
		for _, o := range objs {
			if time.Since(o.arrived) <= limit {
				fresh = append(fresh, o)
			}
		}
		objs = fresh
	}
	if !covered(objs, lo, hi, hasEnd, end) {
		return nil, false
	}
	return objs, true
}

func (t *track) knownGroups(first, last uint64) []uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.spilled.between(first, last)
	for id := range t.groups {
		if id >= first && id <= last {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (t *track) properties() message.Properties {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.props
}

type spillIndex struct {
	ids []uint64
}

func (x *spillIndex) add(id uint64) {
	// the group IDs spilled during this process's life, sorted and bounded to the most recent
	i, found := slices.BinarySearch(x.ids, id)
	if found {
		return
	}
	x.ids = slices.Insert(x.ids, i, id)
	if len(x.ids) > spillIndexMax {
		x.ids = slices.Delete(x.ids, 0, len(x.ids)-spillIndexMax)
	}
}

func (x *spillIndex) remove(id uint64) {
	if i, found := slices.BinarySearch(x.ids, id); found {
		x.ids = slices.Delete(x.ids, i, i+1)
	}
}

func (x *spillIndex) has(id uint64) bool {
	_, found := slices.BinarySearch(x.ids, id)
	return found
}

func (x *spillIndex) between(first, last uint64) []uint64 {
	i, _ := slices.BinarySearch(x.ids, first)
	j, found := slices.BinarySearch(x.ids, last)
	if found {
		j++
	}
	return slices.Clone(x.ids[i:j])
}
