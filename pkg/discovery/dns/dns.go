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

// ErrStopped aliases discovery.ErrStopped for callers of this package
var ErrStopped = discovery.ErrStopped

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
	p, err := newProvider(name, o, m)
	if err != nil {
		return nil, err
	}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// provider carries the dns provider's connection-level settings; the
// shared discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	mode     mode
	res      resolver
	interval time.Duration
}

func newProvider(name string, o *do.Options, m mode) (*provider, error) {
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
	return &provider{name: name, mode: m, res: r, interval: interval}, nil
}

// newSubscription builds a query's poll-loop runner; it satisfies
// discovery.NewSubscriptionFunc
func (p *provider) newSubscription(q *do.Query, handler discovery.SnapshotHandler) (discovery.SubscriptionRunner, error) {
	return &subscription{p: p, q: q, emitter: discovery.NewEmitter(handler)}, nil
}

// subscription is one query's poll loop; it implements
// discovery.SubscriptionRunner
type subscription struct {
	p       *provider
	q       *do.Query
	emitter *discovery.Emitter

	mtx     sync.Mutex
	cancel  context.CancelFunc
	stopped bool
	failing bool
}

// Launch starts the query's poll loop
func (s *subscription) Launch(ctx context.Context) {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mtx.Unlock()
	go s.run(subCtx)
}

// Stop terminates the poll loop and suppresses further emissions
func (s *subscription) Stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.mtx.Unlock()
	s.emitter.Stop()
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
		wait := s.p.interval
		snap, ttl, err := s.resolve(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.warnResolve(err)
		} else {
			s.clearWarn()
			s.emitter.Emit(snap)
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
	switch s.p.mode {
	case modeSRV:
		return s.resolveSRV(ctx)
	default:
		return s.resolveA(ctx)
	}
}

// resolveSRV maps the highest-priority tier of the SRV answer onto members
func (s *subscription) resolveSRV(ctx context.Context) (discovery.Snapshot, time.Duration, error) {
	answers, ttl, err := s.p.res.lookupSRV(ctx, dns.Fqdn(s.q.SRVName))
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
	ips, ttl, err := s.p.res.lookupIP(ctx, dns.Fqdn(s.q.Hostname))
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

// warnResolve counts a resolution failure and logs it once per failure
// streak; the last-good membership keeps serving
func (s *subscription) warnResolve(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, s.p.providerName()).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("dns discovery resolution failed; keeping last-good members",
		logging.Pairs{
			"discoverer": s.p.name,
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
			logging.Pairs{"discoverer": s.p.name, "query": s.queryName()})
		return
	}
	s.mtx.Unlock()
}

func (s *subscription) queryName() string {
	if s.p.mode == modeSRV {
		return s.q.SRVName
	}
	return s.q.Hostname
}

func (p *provider) providerName() string {
	if p.mode == modeSRV {
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
