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
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// tzPosMax bounds the positive cache; the IANA set is ~600 names.
	tzPosMax = 1024
	// tzNegMax bounds the separately held negative cache.
	tzNegMax = 256
	// maxTZLength bounds a tz name before any zoneinfo search happens.
	maxTZLength = 64
	// tzLoadBurst/tzLoadPerSecond bound global cold-load I/O: hostile unique
	// names cannot drive more zoneinfo searches than the refill rate allows.
	tzLoadBurst     = 64
	tzLoadPerSecond = 16
)

type tzCache struct {
	pos      sync.Map // name -> *time.Location
	posCount atomic.Int64
	mu       sync.Mutex // guards the fields below; never held during I/O
	neg      map[string]*list.Element
	negLL    *list.List // front = most recently used negative
	inflight map[string]*tzLoad
	// cold-load token bucket
	tokens     float64
	lastRefill time.Time
	// loader is time.LoadLocation unless a test injects one
	loader func(string) (*time.Location, error)
	now    func() time.Time
}

// tzLoad coalesces concurrent cold lookups of one name
type tzLoad struct {
	done chan struct{}
	loc  *time.Location // nil = negative; valid after done is closed
}

// tzResult is the outcome of a tzCache lookup: a rate-limited cold lookup is
// undetermined, so the caller must not substitute another zone for it
type tzResult int

const (
	// tzValid: the name resolved to the returned *time.Location
	tzValid tzResult = iota
	// tzInvalid: the name is definitively not a loadable zone
	tzInvalid
	// tzUnavailable: the cold-load budget is spent and the name's validity
	// was not determined; nothing was cached
	tzUnavailable
)

func (c *tzCache) get(name string) (*time.Location, tzResult) {
	if len(name) > maxTZLength {
		// no real IANA name approaches this length: invalid, with no search
		return nil, tzInvalid
	}
	if v, ok := c.pos.Load(name); ok {
		return v.(*time.Location), tzValid
	}
	c.mu.Lock()
	if c.neg == nil {
		c.neg = make(map[string]*list.Element)
		c.negLL = list.New()
	}
	if el, ok := c.neg[name]; ok {
		c.negLL.MoveToFront(el)
		c.mu.Unlock()
		return nil, tzInvalid
	}
	if c.inflight == nil {
		c.inflight = make(map[string]*tzLoad)
	}
	if call, ok := c.inflight[name]; ok {
		c.mu.Unlock()
		<-call.done
		if call.loc == nil {
			return nil, tzInvalid
		}
		return call.loc, tzValid
	}
	if !c.takeTokenLocked() {
		// budget spent, validity unknown: report unavailable (never invalid)
		// and cache nothing, so hostile names cannot exceed the refill rate
		c.mu.Unlock()
		return nil, tzUnavailable
	}
	call := &tzLoad{done: make(chan struct{})}
	c.inflight[name] = call
	c.mu.Unlock()

	load := c.loader
	if load == nil {
		load = time.LoadLocation
	}
	loc, err := load(name) // outside every lock

	c.mu.Lock()
	delete(c.inflight, name)
	if err != nil {
		c.neg[name] = c.negLL.PushFront(name)
		if c.negLL.Len() > tzNegMax {
			oldest := c.negLL.Back()
			c.negLL.Remove(oldest)
			delete(c.neg, oldest.Value.(string))
		}
	}
	c.mu.Unlock()
	if err == nil {
		if c.posCount.Load() < tzPosMax {
			if actual, loaded := c.pos.LoadOrStore(name, loc); loaded {
				loc = actual.(*time.Location)
			} else {
				c.posCount.Add(1)
			}
		} else if v, ok := c.pos.Load(name); ok {
			loc = v.(*time.Location)
		}
		call.loc = loc
	}
	close(call.done)
	if call.loc == nil {
		return nil, tzInvalid
	}
	return call.loc, tzValid
}

func (c *tzCache) takeTokenLocked() bool {
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.lastRefill.IsZero() {
		c.tokens = tzLoadBurst
	} else {
		c.tokens = min(tzLoadBurst, c.tokens+now.Sub(c.lastRefill).Seconds()*tzLoadPerSecond)
	}
	c.lastRefill = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}
