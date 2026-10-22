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
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// at most this many refused tracks are remembered per relay, so a flood of names that do not exist cannot grow it
const maxNegatives = 4096

type negatives struct {
	ttl   atomic.Int64 // nanoseconds a refusal is remembered; 0 remembers none
	count atomic.Int32 // lets a request skip the lock while nothing is remembered

	mu sync.Mutex
	m  map[string]negative
}

type negative struct {
	ns     wire.Namespace
	reason string
	until  time.Time
}

// SetNegativeTTL sets how long the relay answers for a track the origin refused with DOES_NOT_EXIST, without
// asking it again; 0 remembers no refusal, and forgets those remembered
func (r *Relay) SetNegativeTTL(d time.Duration) {
	n := &r.negs
	n.ttl.Store(int64(max(d, 0)))
	if d > 0 {
		return
	}
	n.mu.Lock()
	n.m = nil
	n.count.Store(0)
	n.mu.Unlock()
}

func (n *negatives) refusal(t *track) error {
	// the origin's refusal, while it is remembered, of a track it said does not exist
	if n.count.Load() == 0 || n.ttl.Load() == 0 {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.m[t.key]
	if !ok {
		return nil
	}
	if time.Now().After(e.until) {
		n.deleteLocked(t.key)
		return nil
	}
	return &requestError{code: message.RequestDoesNotExist, reason: e.reason}
}

func (n *negatives) record(t *track, err error) {
	re, ok := errors.AsType[*requestError](err)
	ttl := time.Duration(n.ttl.Load())
	if !ok || re.code != message.RequestDoesNotExist || ttl == 0 {
		return
	}
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, known := n.m[t.key]; !known && len(n.m) >= maxNegatives {
		for k, e := range n.m {
			if now.After(e.until) {
				n.deleteLocked(k)
			}
		}
		if len(n.m) >= maxNegatives {
			return
		}
	}
	if n.m == nil {
		n.m = make(map[string]negative)
	}
	n.m[t.key] = negative{ns: t.name.Namespace.Clone(), reason: re.reason, until: now.Add(ttl)}
	n.count.Store(int32(len(n.m))) // #nosec G115 -- bounded by maxNegatives
}

func (n *negatives) forget(t *track) {
	// a publisher's PUBLISH of the track means it exists now
	if n.count.Load() == 0 {
		return
	}
	n.mu.Lock()
	n.deleteLocked(t.key)
	n.mu.Unlock()
}

func (n *negatives) forgetUnder(prefix wire.Namespace) {
	// a namespace arriving may hold any track remembered under it
	if n.count.Load() == 0 {
		return
	}
	n.mu.Lock()
	for k, e := range n.m {
		if e.ns.HasPrefix(prefix) {
			n.deleteLocked(k)
		}
	}
	n.mu.Unlock()
}

func (n *negatives) deleteLocked(key string) {
	delete(n.m, key)
	n.count.Store(int32(len(n.m))) // #nosec G115 -- bounded by maxNegatives
}
