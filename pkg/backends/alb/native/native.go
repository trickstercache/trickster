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

// Package native adapts a load balancer's selection strategy to the listeners that speak a
// backend's own protocol: it commits each authenticated session to the pool member the
// strategy picks, for as long as the session lasts.
package native

import (
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
)

// Resolver returns a route resolver that balances sessions with picker, which o configures, and
// keeps each client's sessions on the member its key is pinned to when persist is set.
// A session is one unit of work: it counts against its member until its route is released.
func Resolver(picker lb.Picker, o *ao.Options, persist *sticky.Flows) backends.RouteResolver {
	if picker == nil {
		return nil
	}
	return &resolver{picker: picker, options: o, sticky: persist}
}

type resolver struct {
	picker  lb.Picker
	options *ao.Options
	sticky  *sticky.Flows
}

var unavailable = backends.RouteDecision{Outcome: backends.RouteOutcomeUnavailable}

func (r *resolver) ResolveRoute(in backends.RouteInput) (backends.RouteDecision, bool) {
	var s *sticky.FlowSession
	if r.sticky != nil {
		s = new(sticky.FlowSession)
		r.sticky.Begin(s, r.sticky.SessionKey(principal(in), in.Client), time.Now().UnixNano())
	}
	// each load balancer passed through on the way to a member reads the key its own way
	flowOf := func(depth int, p lb.Picker, via *lb.Member) lb.Flow {
		var f lb.Flow
		if p.Needs().Has(lb.NeedKey) {
			o := r.options
			if via != nil {
				o = optionsOf(via)
			}
			f = key(o, in)
		}
		if s != nil {
			f = s.Flow(depth, p, via, f)
		}
		return f
	}
	pk, ok := lb.PickLeafFunc(r.picker, flowOf)
	if s != nil {
		stranded := !ok && s.Stranded()
		s.Picked(pk)
		if s.Rejects() && s.Unavailable() {
			if ok {
				pk.Done(lb.OutcomeCanceled)
			}
			s.Refuse()
			return unavailable, false
		}
		if stranded {
			// the pinned pool has no member left: the session moves to a pool that does
			pk, ok = lb.RepickLeafFunc(r.picker, flowOf)
			s.Picked(pk)
		}
	}
	if !ok {
		return unavailable, false
	}
	t, ok := pk.Member().Value.(*pool.Target)
	if !ok || t.Backend() == nil {
		pk.Done(lb.OutcomeCanceled)
		return unavailable, false
	}
	if s != nil {
		// the member takes the session as it is handed over: a native member dials its own
		// origin later, and reconnects to it, as each session's statements need it
		s.Settle()
	}
	var once sync.Once
	// the target carries no status: the pool has already held the member to the load balancer's
	// healthy_floor, which a second, fixed threshold must not overrule
	return backends.RouteDecision{
		Target:  backends.RouteTarget{Backend: t.Backend()},
		Outcome: backends.RouteOutcomeSelected,
		Release: func() { once.Do(func() { pk.Done(lb.OutcomeOK) }) },
	}, true
}

func optionsOf(m *lb.Member) *ao.Options {
	if t, ok := m.Value.(*pool.Target); ok && t.Backend() != nil {
		if cfg := t.Backend().Configuration(); cfg != nil {
			return cfg.ALBOptions
		}
	}
	return nil
}

// principal is the name a session authenticated as; one the protocol did not verify keys nothing
func principal(in backends.RouteInput) string {
	if !in.Authenticated {
		return ""
	}
	return in.Username
}

// key is the session's affinity key as one load balancer is configured to read it: the name
// it authenticated as, or else the client's address
func key(o *ao.Options, in backends.RouteInput) lb.Flow {
	var ks flowkey.KeySource
	prefix := flowkey.DefaultIPv6Prefix
	if o != nil {
		ks = o.HRW.KeySource
		if o.HRW.IPv6Prefix > 0 {
			prefix = o.HRW.IPv6Prefix
		}
	}
	v := flowkey.Session(ks, prefix, principal(in), in.Client)
	return lb.Flow{Key: v.Hash, HasKey: v.OK}
}
