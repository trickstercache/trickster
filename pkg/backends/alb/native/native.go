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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Resolver returns a route resolver that balances sessions with picker, which o configures.
// A session is one unit of work: it counts against its member until its route is released.
func Resolver(picker lb.Picker, o *ao.Options) backends.RouteResolver {
	if picker == nil {
		return nil
	}
	return &resolver{picker: picker, options: o}
}

type resolver struct {
	picker  lb.Picker
	options *ao.Options
}

func (r *resolver) ResolveRoute(in backends.RouteInput) (backends.RouteDecision, bool) {
	// each load balancer passed through on the way to a member reads the key its own way
	flowOf := func(p lb.Picker, via *lb.Member) lb.Flow {
		if !p.Needs().Has(lb.NeedKey) {
			return lb.Flow{}
		}
		o := r.options
		if via != nil {
			o = optionsOf(via)
		}
		return key(o, in)
	}
	pk, ok := lb.PickLeafFunc(r.picker, flowOf)
	if !ok {
		return backends.RouteDecision{Outcome: backends.RouteOutcomeUnavailable}, false
	}
	t, ok := pk.Member().Value.(*pool.Target)
	if !ok || t.Backend() == nil {
		pk.Done(lb.OutcomeCanceled)
		return backends.RouteDecision{Outcome: backends.RouteOutcomeUnavailable}, false
	}
	var once sync.Once
	d := backends.RouteDecision{
		Target:  backends.RouteTarget{Backend: t.Backend()},
		Outcome: backends.RouteOutcomeSelected,
		Release: func() { once.Do(func() { pk.Done(lb.OutcomeOK) }) },
	}
	if st := t.HealthStatus(); st != nil {
		// a nil status must not become a non-nil interface
		d.Target.Status = st
	}
	return d, true
}

func optionsOf(m *lb.Member) *ao.Options {
	if t, ok := m.Value.(*pool.Target); ok && t.Backend() != nil {
		if cfg := t.Backend().Configuration(); cfg != nil {
			return cfg.ALBOptions
		}
	}
	return nil
}

// key is the session's affinity key as one load balancer is configured to read it: the name
// it authenticated as, or else the client's address
func key(o *ao.Options, in backends.RouteInput) lb.Flow {
	prefix := ao.DefaultIPv6Prefix
	if o != nil {
		if o.HRW.KeySource.Kind == ao.KeyUser {
			if in.Username == "" {
				return lb.Flow{}
			}
			return lb.Flow{Key: lb.HashString(in.Username), HasKey: true}
		}
		if o.HRW.IPv6Prefix > 0 {
			prefix = o.HRW.IPv6Prefix
		}
	}
	if !in.Client.IsValid() {
		return lb.Flow{}
	}
	return lb.Flow{Key: lb.HashAddr(in.Client.Unmap(), prefix), HasKey: true}
}
