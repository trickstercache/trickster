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

// Package relay is a MoQ Transport relay: it aggregates the downstream subscriptions to a track into one upstream
// subscription, keeps each track's live edge in memory, spills older groups to a cache, and serves FETCH and fills.
package relay

import (
	"container/list"
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

const (
	defaultLiveEdgeGroups   = 3
	defaultLiveEdgeBytes    = 64 << 20
	defaultMaxTTL           = time.Hour
	defaultFillTimeout      = 2 * time.Second
	maxFillTimeout          = 30 * time.Second
	defaultLinger           = 5 * time.Second
	defaultMaxObjectSize    = 64 << 20
	defaultMaxGapFetches    = 16
	defaultReconnectBackoff = time.Second
	defaultMaxRendezvous    = 30 * time.Second
	reconnectAttempts       = 5
	spillQueueLen           = 256
	finishWait              = 2 * time.Second
)

// Scope is the authority and path a downstream session connected to, which select the origin of its tracks
type Scope struct {
	Authority string
	Path      string
}

// Upstream connects the relay to the origins of a scope's tracks
type Upstream interface {
	// Session returns a ready session toward an origin serving track in scope, dialing one when needed, and a release
	// the relay calls once the work it carries there ends: with nil, or with a failure a retry elsewhere might cure
	Session(ctx context.Context, scope Scope, track message.FullTrackName) (*session.Session, func(error), error)
}

// CacheProvider resolves the cache that older groups spill to, at each use, so a reload can change it
type CacheProvider interface {
	Cache() cache.Cache
}

// Options configure a relay
type Options struct {
	// LiveEdgeGroups is how many groups of each track the in-memory live edge keeps
	LiveEdgeGroups int
	// LiveEdgeBytes bounds the payload bytes the live edge holds across all tracks
	LiveEdgeBytes int64
	// Spill writes groups leaving the live edge, and groups fetched upstream, to the cache
	Spill bool
	// MaxTTL caps how long a spilled group lives, and is the lifetime of one whose track sets none
	MaxTTL time.Duration
	// FillTimeout bounds the wait for upstream fetches when a FETCH or fill names no FILL_TIMEOUT
	FillTimeout time.Duration
	// Prewarm keeps an upstream subscription forwarding while every downstream subscriber is paused
	Prewarm bool
	// Linger keeps an upstream subscription this long after its last downstream subscriber leaves
	Linger time.Duration
	// MaxObjectSize bounds an object's payload; a larger one resets the stream carrying it
	MaxObjectSize int64
	// MaxGapFetches bounds the upstream fetches one FETCH or fill issues for objects the relay lacks
	MaxGapFetches int
	// ReconnectBackoff is the first wait before re-subscribing upstream after an upstream subscription is lost
	ReconnectBackoff time.Duration
	// MaxRendezvous caps how long a SUBSCRIBE waits for a publisher, whatever its RENDEZVOUS_TIMEOUT
	MaxRendezvous time.Duration
	// Metrics receives the relay's measurements; nil records none
	Metrics *Metrics
}

func (o Options) withDefaults() Options {
	if o.LiveEdgeGroups <= 0 {
		o.LiveEdgeGroups = defaultLiveEdgeGroups
	}
	if o.LiveEdgeBytes <= 0 {
		o.LiveEdgeBytes = defaultLiveEdgeBytes
	}
	if o.MaxTTL <= 0 {
		o.MaxTTL = defaultMaxTTL
	}
	if o.FillTimeout <= 0 {
		o.FillTimeout = defaultFillTimeout
	}
	if o.Linger <= 0 {
		o.Linger = defaultLinger
	}
	if o.MaxObjectSize <= 0 {
		o.MaxObjectSize = defaultMaxObjectSize
	}
	if o.MaxGapFetches <= 0 {
		o.MaxGapFetches = defaultMaxGapFetches
	}
	if o.ReconnectBackoff <= 0 {
		o.ReconnectBackoff = defaultReconnectBackoff
	}
	if o.MaxRendezvous <= 0 {
		o.MaxRendezvous = defaultMaxRendezvous
	}
	return o
}

// Relay serves the downstream sessions of one backend
type Relay struct {
	opts      Options
	maxObject uint64
	up        Upstream
	caches    CacheProvider
	m         *meters
	tracks    table

	ctx    context.Context
	cancel context.CancelFunc

	lruMu sync.Mutex
	lru   list.List
	bytes atomic.Int64

	evictCh chan struct{}
	spillCh chan spillJob
	wg      sync.WaitGroup

	acc atomic.Pointer[Access]
	ns  namespaces
}

// New returns a relay whose tracks come from up and spill to the cache c resolves; up may be nil for a relay
// whose tracks are only published to it, and c may be nil when nothing spills
func New(o Options, up Upstream, c CacheProvider) *Relay {
	o = o.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	r := &Relay{
		opts: o, up: up, caches: c, m: o.Metrics.resolve(), ctx: ctx, cancel: cancel,
		evictCh: make(chan struct{}, 1), spillCh: make(chan spillJob, spillQueueLen),
	}
	r.maxObject = uint64(o.MaxObjectSize) // #nosec G115 -- withDefaults leaves it positive
	r.tracks.init()
	r.wg.Add(1)
	go r.maintain()
	return r
}

// Close ends the relay's upstream subscriptions and background work; downstream sessions are closed by whoever
// accepted them
func (r *Relay) Close() {
	r.cancel()
	r.tracks.each(func(t *track) { t.shutdown() })
	r.wg.Wait()
}

// Handler returns the session.Handler for a downstream session connected to scope
func (r *Relay) Handler(scope Scope) session.Handler {
	return &downstream{r: r, scope: scope}
}

func millis(ms uint64) time.Duration {
	// a millisecond count from the wire, clamped so a hostile value cannot overflow
	if ms > uint64(math.MaxInt64/int64(time.Millisecond)) {
		return math.MaxInt64
	}
	return time.Duration(ms) * time.Millisecond // #nosec G115 -- bounded above
}

func (r *Relay) cache() cache.Cache {
	if r.caches == nil {
		return nil
	}
	return r.caches.Cache()
}

func (r *Relay) maintain() {
	defer r.wg.Done()
	for {
		select {
		case <-r.evictCh:
			r.evictOverBudget()
		case j := <-r.spillCh:
			j.run(r)
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *Relay) addBytes(n int64) {
	r.m.liveEdge(r.bytes.Add(n))
	if n > 0 && r.bytes.Load() > r.opts.LiveEdgeBytes {
		select {
		case r.evictCh <- struct{}{}:
		default:
		}
	}
}

func (r *Relay) lruAdd(g *group) {
	r.lruMu.Lock()
	g.elem = r.lru.PushBack(g)
	r.lruMu.Unlock()
}

func (r *Relay) lruRemove(g *group) {
	r.lruMu.Lock()
	if g.elem != nil {
		r.lru.Remove(g.elem)
		g.elem = nil
	}
	r.lruMu.Unlock()
}

func (r *Relay) evictOverBudget() {
	// the oldest groups go first, except that each track keeps its newest group while others remain to evict
	for pass := 0; pass < 2 && r.bytes.Load() > r.opts.LiveEdgeBytes; pass++ {
		r.lruMu.Lock()
		var victims []*group
		excess := r.bytes.Load() - r.opts.LiveEdgeBytes
		for e := r.lru.Front(); e != nil && excess > 0; e = e.Next() {
			g := e.Value.(*group)
			if pass == 0 && g.t.isNewest(g) {
				continue
			}
			victims = append(victims, g)
			excess -= g.bytes.Load()
		}
		r.lruMu.Unlock()
		for _, g := range victims {
			g.t.evict(g, true)
		}
	}
}

func (r *Relay) queueSpill(j spillJob) {
	select {
	case r.spillCh <- j:
	default:
		r.m.drop(dropSpillQueueFull)
	}
}
