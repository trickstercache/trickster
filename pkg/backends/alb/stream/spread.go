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

package stream

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

// spreader is a load balancer whose mechanism commits one flow to several members at once
type spreader interface {
	Spread() types.Spread
	Pool() pool.Pool
}

// spreadUpstream commits a flow to several of a pool's available members: a connect race for
// tcp and tls, a copy of every datagram for udp. Members that cannot be dialed are passed over.
type spreadUpstream struct {
	lb     spreader
	kind   types.Spread
	width  int
	next   atomic.Uint64
	series seriesCache
}

func newSpread(sp spreader, cfg *bo.Options) *spreadUpstream {
	u := &spreadUpstream{lb: sp, kind: sp.Spread(), width: ao.DefaultRaceWidth}
	if cfg != nil && cfg.ALBOptions != nil && cfg.ALBOptions.Stream != nil && cfg.ALBOptions.Stream.RaceWidth > 0 {
		u.width = cfg.ALBOptions.Stream.RaceWidth
	}
	return u
}

// dialable returns the pool's available members that have an address to connect to. The
// pool is read on every flow, since discovery and reloads replace it.
func (u *spreadUpstream) dialable() pool.Targets {
	p := u.lb.Pool()
	if p == nil {
		return nil
	}
	live := p.Targets()
	for i, t := range live {
		if t.Dialable() {
			continue
		}
		// rare: copy the shared slice only when something must be left out of it
		out := append(make(pool.Targets, 0, len(live)-1), live[:i]...)
		for _, rest := range live[i+1:] {
			if rest.Dialable() {
				out = append(out, rest)
			}
		}
		return out
	}
	return live
}

func (u *spreadUpstream) routeTo(t *pool.Target, f l4.Flow) *spreadRoute {
	return &spreadRoute{addr: t.Addr(), series: u.series.seriesFor(t.Member(), f, t.Name())}
}

// Pick returns the flow's one answering route: the first available member. A race is asked
// for its routes through Race instead.
func (u *spreadUpstream) Pick(f l4.Flow) (l4.Route, bool) {
	live := u.dialable()
	if len(live) == 0 {
		return nil, false
	}
	return u.routeTo(live[0], f), true
}

// Race returns up to the configured width of members to connect to at once, starting one
// member further on with each flow so a pool wider than the race shares its connects.
func (u *spreadUpstream) Race(f l4.Flow) []l4.Route {
	live := u.dialable()
	if len(live) == 0 {
		return nil
	}
	if u.kind != types.SpreadRace {
		return []l4.Route{u.routeTo(live[0], f)}
	}
	n := min(len(live), u.width)
	start := int(u.next.Add(1) % uint64(len(live))) // #nosec G115 -- bounded by the pool's length
	routes := make([]l4.Route, n)
	for i := range routes {
		routes[i] = u.routeTo(live[(start+i)%len(live)], f)
	}
	return routes
}

// Mirror returns the members beside the first, which answers the flow, to copy it to.
func (u *spreadUpstream) Mirror(f l4.Flow, _ l4.Route) []l4.Route {
	live := u.dialable()
	if u.kind != types.SpreadMirror || len(live) < 2 {
		return nil
	}
	routes := make([]l4.Route, len(live)-1)
	for i, t := range live[1:] {
		routes[i] = u.routeTo(t, f)
	}
	return routes
}

// spreadRoute is one member's part in a flow that was committed to several
type spreadRoute struct {
	addr   string
	series *memberSeries
}

func (r *spreadRoute) Addr() string { return r.addr }

// Final is true: the flow's other members were tried along with this one
func (r *spreadRoute) Final() bool { return true }

func (r *spreadRoute) Dialed(d time.Duration, err error) {
	switch {
	case err == nil:
		r.series.connect.Observe(d.Seconds())
		r.series.active.Inc()
	case errors.Is(err, l4.ErrAbandoned):
	default:
		r.series.dialFailed.Inc()
	}
}

func (r *spreadRoute) FirstByte() {}

func (r *spreadRoute) Closed(err error) {
	r.series.active.Dec()
	if err != nil {
		r.series.unreachable.Inc()
		return
	}
	r.series.proxied.Inc()
}
