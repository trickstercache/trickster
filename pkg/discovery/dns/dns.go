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

// Package dns implements the dns_srv and dns_a autodiscovery providers.
// Records are re-resolved on a poll cadence (discovery.<name>.dns.interval,
// default 30s) with record TTLs acting as a floor: an answer is never
// re-resolved before its shortest TTL expires. Resolution failures keep the
// last-good membership.
//
// dns_srv maps SRV target/port onto member addresses and SRV weight onto
// the member load-balancing weight. Only the highest-priority tier (the
// lowest SRV priority value present in the answer) becomes members; lower
// tiers are standby capacity and are ignored in this version, so a tier
// failure is handled by DNS-side record updates rather than by Trickster.
//
// dns_a resolves A/AAAA records with a fixed port and scheme from the
// query, covering headless-service-style DNS outside kubernetes, Docker
// embedded DNS, and Consul DNS interfaces without a bespoke provider.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/miekg/dns"
)

// ErrStopped is returned when subscribing to a stopped discoverer
var ErrStopped = errors.New("dns discoverer is stopped")

type mode int8

const (
	modeSRV mode = iota
	modeA
)

// NewSRV constructs the dns_srv Discoverer; it satisfies
// discovery.NewDiscovererFunc
func NewSRV(name string, o *do.Options) (discovery.Discoverer, error) {
	return newDiscoverer(name, o, modeSRV)
}

// NewA constructs the dns_a Discoverer; it satisfies
// discovery.NewDiscovererFunc
func NewA(name string, o *do.Options) (discovery.Discoverer, error) {
	return newDiscoverer(name, o, modeA)
}

func newDiscoverer(name string, o *do.Options, m mode) (discovery.Discoverer, error) {
	if o == nil || o.DNS == nil {
		return nil, fmt.Errorf("no dns options provided for discoverer %q", name)
	}
	interval := time.Duration(o.DNS.Interval)
	if interval <= 0 {
		interval = do.DefaultDNSInterval
	}
	r, err := newResolver(o.DNS.Resolver)
	if err != nil {
		return nil, err
	}
	return &discoverer{name: name, mode: m, res: r, interval: interval}, nil
}

type discoverer struct {
	name     string
	mode     mode
	res      resolver
	interval time.Duration

	mtx     sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	subs    map[*subscription]struct{}
	started bool
	stopped bool
}

func (d *discoverer) Start(ctx context.Context) error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return ErrStopped
	}
	if d.started {
		return nil
	}
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.started = true
	for s := range d.subs {
		s.launch(d.ctx)
	}
	return nil
}

func (d *discoverer) Stop() error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return nil
	}
	d.stopped = true
	if d.cancel != nil {
		d.cancel()
	}
	for s := range d.subs {
		s.stop()
	}
	d.subs = nil
	return nil
}

func (d *discoverer) Subscribe(q *do.Query, handler discovery.SnapshotHandler) (func(), error) {
	if q == nil || handler == nil {
		return nil, errors.New("nil query or handler")
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return nil, ErrStopped
	}
	s := &subscription{d: d, q: q.Clone(), handler: handler}
	if d.subs == nil {
		d.subs = make(map[*subscription]struct{})
	}
	d.subs[s] = struct{}{}
	if d.started {
		s.launch(d.ctx)
	}
	unsubscribe := func() {
		d.mtx.Lock()
		delete(d.subs, s)
		d.mtx.Unlock()
		s.stop()
	}
	return unsubscribe, nil
}

// subscription is one query's poll loop
type subscription struct {
	d       *discoverer
	q       *do.Query
	handler discovery.SnapshotHandler

	mtx      sync.Mutex
	cancel   context.CancelFunc
	last     discovery.Snapshot
	hasLast  bool
	launched bool
	stopped  bool
	failing  bool
}

func (s *subscription) launch(ctx context.Context) {
	s.mtx.Lock()
	if s.launched || s.stopped {
		s.mtx.Unlock()
		return
	}
	s.launched = true
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mtx.Unlock()
	go s.run(subCtx)
}

func (s *subscription) stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.mtx.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run is the poll loop: resolve, deliver on change, then sleep for the
// configured interval or the answer's shortest TTL, whichever is longer
func (s *subscription) run(ctx context.Context) {
	timer := time.NewTimer(0) // resolve immediately on launch
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		wait := s.d.interval
		snap, ttl, err := s.resolve(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.warnResolve(err)
		} else {
			s.clearWarn()
			s.deliver(snap)
			if ttl > wait {
				// TTL floor: the answer is authoritative until its shortest
				// TTL expires; re-resolving sooner is wasted load
				wait = ttl
			}
		}
		timer.Reset(wait)
	}
}

func (s *subscription) resolve(ctx context.Context) (discovery.Snapshot, time.Duration, error) {
	switch s.d.mode {
	case modeSRV:
		return s.resolveSRV(ctx)
	default:
		return s.resolveA(ctx)
	}
}

// resolveSRV maps the highest-priority tier of the SRV answer onto members
func (s *subscription) resolveSRV(ctx context.Context) (discovery.Snapshot, time.Duration, error) {
	answers, ttl, err := s.d.res.lookupSRV(ctx, dns.Fqdn(s.q.SRVName))
	if err != nil {
		return nil, 0, err
	}
	minPriority := uint16(0)
	for i, a := range answers {
		if i == 0 || a.Priority < minPriority {
			minPriority = a.Priority
		}
	}
	out := make(discovery.Snapshot, 0, len(answers))
	for _, a := range answers {
		if a.Priority != minPriority || a.Target == "." || a.Target == "" {
			continue
		}
		host := strings.TrimSuffix(a.Target, ".")
		out = append(out, discovery.Member{
			Name:    host,
			Scheme:  schemeOf(s.q),
			Address: net.JoinHostPort(host, strconv.Itoa(int(a.Port))),
			Weight:  int(a.Weight),
			Ready:   discovery.ReadyUnknown,
			Labels: map[string]string{
				"priority": strconv.Itoa(int(a.Priority)),
			},
		})
	}
	return out, ttl, nil
}

// resolveA maps each A/AAAA answer onto a member with the query's fixed
// port and scheme
func (s *subscription) resolveA(ctx context.Context) (discovery.Snapshot, time.Duration, error) {
	ips, ttl, err := s.d.res.lookupIP(ctx, dns.Fqdn(s.q.Hostname))
	if err != nil {
		return nil, 0, err
	}
	out := make(discovery.Snapshot, 0, len(ips))
	for _, ip := range ips {
		out = append(out, discovery.Member{
			Name:    ip,
			Scheme:  schemeOf(s.q),
			Address: net.JoinHostPort(ip, s.q.Port),
			Ready:   discovery.ReadyUnknown,
		})
	}
	return out, ttl, nil
}

// deliver emits the snapshot when membership changed
func (s *subscription) deliver(snap discovery.Snapshot) {
	canonical := snap.Canonical()
	s.mtx.Lock()
	if s.stopped || (s.hasLast && canonical.Equal(s.last)) {
		s.mtx.Unlock()
		return
	}
	s.last = canonical
	s.hasLast = true
	s.mtx.Unlock()
	s.handler(canonical)
}

// warnResolve counts a resolution failure and logs it once per failure
// streak; the last-good membership keeps serving
func (s *subscription) warnResolve(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.d.name, s.d.providerName()).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("dns discovery resolution failed; keeping last-good members",
		logging.Pairs{
			"discoverer": s.d.name,
			"query":      s.queryName(),
			"error":      err.Error(),
		})
}

func (s *subscription) clearWarn() {
	s.mtx.Lock()
	if s.failing {
		s.failing = false
		s.mtx.Unlock()
		discovery.LogInfo("dns discovery resolution recovered",
			logging.Pairs{"discoverer": s.d.name, "query": s.queryName()})
		return
	}
	s.mtx.Unlock()
}

func (s *subscription) queryName() string {
	if s.d.mode == modeSRV {
		return s.q.SRVName
	}
	return s.q.Hostname
}

func (d *discoverer) providerName() string {
	if d.mode == modeSRV {
		return providers.DNSSRV
	}
	return providers.DNSA
}

func schemeOf(q *do.Query) string {
	if q.Scheme != "" {
		return q.Scheme
	}
	return "http"
}
