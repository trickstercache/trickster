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

package moq

import (
	"net/netip"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

const (
	planeMoQ          = "moq"
	reasonRateLimited = "rate limited"
)

type limiter struct {
	lim       *ratelimit.Limiter
	sources   []flowkey.KeySource
	v6Prefix  int
	count     bool
	decisions *metrics.RateLimitDecision
}

func newLimiter(o *rlopts.Options) *limiter {
	// a listener's limiter counts its sessions and a backend's the requests of its sessions, each by client address
	if o == nil {
		return nil
	}
	return &limiter{
		lim: ratelimit.LookupOptions(o), sources: o.KeySources, v6Prefix: o.IPv6Prefix,
		count: o.Action == rlopts.ActionCount, decisions: metrics.NewRateLimitDecision(o.Name, planeMoQ),
	}
}

type limitKey struct {
	hash uint64
	ok   bool
}

func (l *limiter) keyFor(client netip.Addr) limitKey {
	// a limiter naming no keys counts every event in one bucket
	if len(l.sources) == 0 {
		return limitKey{ok: true}
	}
	var k limitKey
	present := 0
	for _, ks := range l.sources {
		v := flowkey.Session(ks, l.v6Prefix, "", client)
		if v.OK {
			present++
		}
		k.hash = lb.Mix(k.hash ^ v.Hash)
	}
	k.ok = present == len(l.sources)
	return k
}

func (l *limiter) admit(k limitKey) (bool, time.Duration) {
	// returns whether the event proceeds and, when it does not, how long until it could
	now := ratelimit.Now()
	var d ratelimit.Decision
	if l.count {
		d = l.lim.Count(k.hash, k.ok, now, 1)
	} else {
		d = l.lim.Take(k.hash, k.ok, now, 1)
	}
	l.decisions.Observe(d.Result)
	if l.count || d.Allowed {
		return true, 0
	}
	return false, d.RetryAfter
}

type limitedRequests struct {
	session.Handler
	lim *limiter
	key limitKey
}

func limitRequests(h session.Handler, lim *limiter, client netip.Addr) session.Handler {
	if lim == nil {
		return h
	}
	return &limitedRequests{Handler: h, lim: lim, key: lim.keyFor(client)}
}

func (l *limitedRequests) HandleRequest(r *session.Request) {
	// each SUBSCRIBE, FETCH and TRACK_STATUS is charged to the backend's limiter, and one over the limit refused with
	// EXCESSIVE_LOAD and the wait until it could pass
	switch r.Message().(type) {
	case *message.Subscribe, *message.Fetch, *message.TrackStatus:
		if ok, wait := l.lim.admit(l.key); !ok {
			r.SetHandlers(refusedRequest{}, nil)
			_ = r.Reject(message.RequestExcessiveLoad, reasonRateLimited, retryInterval(wait))
			return
		}
	}
	l.Handler.HandleRequest(r)
}

func retryInterval(d time.Duration) uint64 {
	// the Retry Interval is the least wait in milliseconds plus one, since 0 asks for no retry at all
	return uint64(max(d.Milliseconds(), 0)) + 1 // #nosec G115 -- not negative
}

type refusedRequest struct{}

func (refusedRequest) HandleMessage(*session.Request, message.Message) {}
func (refusedRequest) HandleClose(*session.Request, error)             {}
