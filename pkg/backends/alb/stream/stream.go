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
// Package stream is the adapter between load-balanced pools and the tcp, tls and udp relay:
// it presents a backend to the relay as an upstream, committing each connection or session to
// the pool member that the backend's selection strategy picks.
package stream

import (
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus"
)

// Results a pool member's connections and sessions are counted under
const (
	ResultProxied     = "proxied"
	ResultDialFailed  = "dial_failed"
	ResultUnreachable = "unreachable"
)

// sessionKeeper is a load balancer that keeps the sessions of its stream and native flows
type sessionKeeper interface {
	StickyFlows() *sticky.Flows
}

// FromBackend returns an upstream that commits each flow to a pick-one ALB's member, nested and
// sticky as configured, or dials the backend's origin host; nil for a backend with neither.
func FromBackend(b backends.Backend) l4.Upstream {
	if b == nil {
		return nil
	}
	cfg := b.Configuration()
	if sp, ok := b.(spreader); ok && sp.Spread() != 0 {
		return newSpread(sp, cfg)
	}
	if pp, ok := b.(lb.PickerProvider); ok {
		if p := pp.Picker(); p != nil {
			u := &upstream{picker: p}
			if sk, ok := b.(sessionKeeper); ok {
				u.sticky = sk.StickyFlows()
			}
			if cfg != nil && cfg.ALBOptions != nil {
				u.options = cfg.ALBOptions
				if s := cfg.ALBOptions.Stream; s != nil {
					u.retries = s.ConnectRetries
				}
			}
			return u
		}
	}
	if cfg == nil || cfg.Host == "" {
		return nil
	}
	return l4.Static(cfg.Host)
}

type upstream struct {
	picker lb.Picker
	// the options of the load balancer the listener is bound to; its members that are load
	// balancers themselves are keyed and timed by their own
	options *ao.Options
	retries int
	// keeps each flow on the path its key is pinned to; nil keeps no sessions
	sticky *sticky.Flows
	series seriesCache
}

// seriesCache holds the series of each member a listener has dialed, resolved once per member
// rather than looked up by label on every connection
type seriesCache struct {
	series   sync.Map
	seriesOf atomic.Int64
}

// memberSeries are one member's series for one listener
type memberSeries struct {
	active                           prometheus.Gauge
	connect                          prometheus.Observer
	proxied, dialFailed, unreachable prometheus.Counter
}

// maxCachedSeries bounds the cache: members come and go under discovery, and a member that
// has left is never looked up again, so the cache is simply dropped when it has grown large
const maxCachedSeries = 1024

// seriesFor returns the member's series. The cache is keyed by the member itself, not its
// name: a member that leaves has its series deleted, and one that returns under the same
// name is a new member whose series must be resolved afresh.
func (u *seriesCache) seriesFor(m *lb.Member, f l4.Flow, name string) *memberSeries {
	if v, ok := u.series.Load(m); ok {
		return v.(*memberSeries)
	}
	if u.seriesOf.Add(1) > maxCachedSeries {
		u.series.Clear()
		u.seriesOf.Store(1)
	}
	count := func(result string) prometheus.Counter {
		return metrics.ProxyStreamMemberConnections.WithLabelValues(f.Listener, f.Protocol, name, result)
	}
	s := &memberSeries{
		active:      metrics.ProxyStreamMemberActiveConnections.WithLabelValues(f.Listener, f.Protocol, name),
		connect:     metrics.ProxyStreamMemberConnectDuration.WithLabelValues(f.Listener, f.Protocol, name),
		proxied:     count(ResultProxied),
		dialFailed:  count(ResultDialFailed),
		unreachable: count(ResultUnreachable),
	}
	v, _ := u.series.LoadOrStore(m, s)
	return v.(*memberSeries)
}

func (u *upstream) Pick(f l4.Flow) (l4.Route, bool) {
	return u.pick(f, nil)
}

// retryState is what a route that is a retry carries; a flow's first route has none
type retryState struct {
	attempt int
	// every member the flow failed to reach before this route
	tried []*lb.Member
	// the flow's session, carried from its first route
	session *sticky.FlowSession
}

// Retry offers another member when a connection could not reach the one it was given, as
// far as stream.connect_retries allows, and never one the flow has already been offered. A
// flow whose pinned member could not be reached is refused when its session must not move.
func (u *upstream) Retry(f l4.Flow, failed l4.Route) (l4.Route, bool) {
	prev, ok := failed.(*route)
	if !ok {
		return nil, false
	}
	if s := prev.session; s != nil && s.Rejects() && s.OnPins() {
		s.Refuse()
		return nil, false
	}
	next := &retryState{attempt: 1, session: prev.session}
	if prev.retry != nil {
		next.attempt = prev.retry.attempt + 1
		next.tried = slices.Clip(prev.retry.tried)
	}
	if next.attempt > u.retries {
		return nil, false
	}
	next.tried = append(next.tried, prev.pick.Member())
	return u.pick(f, next)
}

func (u *upstream) pick(f l4.Flow, retry *retryState) (l4.Route, bool) {
	r := &route{retry: retry, udp: f.Protocol == l4.ProtocolUDP}
	var tried []*lb.Member
	switch {
	case retry != nil:
		tried, r.session = retry.tried, retry.session
	case u.sticky != nil:
		r.session = new(sticky.FlowSession)
		u.sticky.Begin(r.session, u.sticky.StreamKey(f), time.Now().UnixNano())
	}
	flowOf := func(depth int, p lb.Picker, via *lb.Member) lb.Flow {
		o := u.options
		if via != nil {
			o = optionsOf(via)
		}
		// a retry may try several members at one depth; the last one asked is the one kept
		r.onConnect[depth] = timesConnect(o, f.Protocol)
		var lf lb.Flow
		if p.Needs().Has(lb.NeedKey) {
			lf = key(o, f)
		}
		if r.session != nil {
			// the load balancer the listener maps to keeps the session's whole path
			lf = r.session.Flow(depth, p, via, lf)
		}
		return lf
	}
	var pk lb.LeafPick
	var ok bool
	if len(tried) == 0 {
		pk, ok = lb.PickLeafFunc(u.picker, flowOf)
	} else {
		pk, ok = lb.RepickLeafFunc(u.picker, flowOf, tried...)
	}
	if s := r.session; s != nil {
		stranded := !ok && len(tried) == 0 && s.Stranded()
		s.Picked(pk)
		if s.Rejects() && s.Unavailable() {
			if ok {
				pk.Done(lb.OutcomeCanceled)
			}
			s.Refuse()
			return nil, false
		}
		if stranded {
			// the pinned pool has no member left: the session moves to a pool that does
			pk, ok = lb.RepickLeafFunc(u.picker, flowOf)
			s.Picked(pk)
		}
	}
	if !ok {
		return nil, false
	}
	if retry != nil && slices.Contains(tried, pk.Member()) {
		// a picker that cannot avoid members offered one the flow has already failed to reach
		pk.Done(lb.OutcomeCanceled)
		return nil, false
	}
	t, ok := pk.Member().Value.(*pool.Target)
	if !ok || !t.Dialable() {
		// the member holds its share and refuses it; that is no fault of the member's
		pk.Done(lb.OutcomeCanceled)
		return nil, false
	}
	r.pick, r.addr = pk, t.Addr()
	r.series = u.series.seriesFor(pk.Member(), f, t.Name())
	return r, true
}

// optionsOf returns the options of the load balancer that a pool member is, or nil
func optionsOf(m *lb.Member) *ao.Options {
	if t, ok := m.Value.(*pool.Target); ok && t.Backend() != nil {
		if cfg := t.Backend().Configuration(); cfg != nil {
			return cfg.ALBOptions
		}
	}
	return nil
}

// key is the flow's affinity key as one load balancer is configured to read it: the server
// name a tls client offered, a PROXY protocol TLV, or else the client's address, never its port
func key(o *ao.Options, f l4.Flow) lb.Flow {
	var ks flowkey.KeySource
	prefix := flowkey.DefaultIPv6Prefix
	if o != nil {
		ks = o.HRW.KeySource
		if o.HRW.IPv6Prefix > 0 {
			prefix = o.HRW.IPv6Prefix
		}
	}
	v := flowkey.StreamValue(ks, prefix, f)
	return lb.Flow{Key: v.Hash, HasKey: v.OK}
}

// timesConnect reports whether one load balancer samples latency at the connect, which is a
// tcp or tls listener's default, rather than at the member's first byte or datagram
func timesConnect(o *ao.Options, protocol string) bool {
	if o == nil {
		return protocol != l4.ProtocolUDP
	}
	signal, err := o.LTSignalFor(protocol)
	return err == nil && signal == ao.LTSignalConnect
}

// route is one flow's commitment to a member, allocated once per connection or session
type route struct {
	pick   lb.LeafPick
	addr   string
	series *memberSeries
	// for each load balancer passed through, whether it times the connect or the first byte
	onConnect [lb.MaxPickDepth]bool
	udp       bool
	// nil unless the route is a retry
	retry *retryState
	// nil unless the load balancer keeps sessions; shared by the flow's retries
	session *sticky.FlowSession
}

// reached notes that the route's member was reached, which settles the flow's session there
func (r *route) reached() {
	r.pick.Reached()
	if r.session != nil {
		r.session.Settle()
	}
}

func (r *route) Addr() string { return r.addr }

// Final is false: a pool may have another member to try
func (r *route) Final() bool { return false }

func (r *route) Dialed(d time.Duration, err error) {
	switch {
	case err == nil:
		if !r.udp {
			// a udp socket opens whether or not anything listens; a reply is what reaches
			r.reached()
		}
		for i := range r.pick.Depth() {
			if r.onConnect[i] {
				r.pick.Level(i).Established(d)
			}
		}
		r.series.connect.Observe(d.Seconds())
		r.series.active.Inc()
	case errors.Is(err, l4.ErrAbandoned):
		r.pick.Done(lb.OutcomeCanceled)
	default:
		r.pick.Done(lb.OutcomeConnectFailed)
		r.series.dialFailed.Inc()
	}
}

func (r *route) FirstByte() {
	r.reached()
	for i := range r.pick.Depth() {
		if !r.onConnect[i] {
			r.pick.Level(i).FirstByte()
		}
	}
}

func (r *route) Closed(err error) {
	r.series.active.Dec()
	if err != nil {
		r.pick.Done(lb.OutcomeConnectFailed)
		r.series.unreachable.Inc()
		return
	}
	if r.udp {
		// a session that ended without a port-unreachable is all a one-way member ever shows
		r.reached()
	}
	r.pick.Done(lb.OutcomeOK)
	r.series.proxied.Inc()
}
