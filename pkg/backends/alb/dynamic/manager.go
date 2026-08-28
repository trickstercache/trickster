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

// Package dynamic manages the discovered member set of a discovery-backed
// ALB at runtime: it consumes membership Snapshots from a discoverer
// subscription, applies guardrails (min_members floor, debounce damping),
// instantiates a full backend client per added member from the ALB's
// template backend, tears down removed members (health checks, idle
// connections, metrics series), and swaps the ALB pool atomically. All of
// this is control-plane work; the request hot path only ever sees complete,
// immutable pool snapshots through the ALB mechanism's atomic pool holder.
package dynamic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/discovery/template"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Config carries the dependencies a Manager needs to instantiate and tear
// down member backends at runtime
type Config struct {
	// ALB is the discovery-backed ALB client whose pool this Manager drives
	ALB *alb.Client
	// Options is the ALB's validated alb.discovery block
	Options *ao.DiscoveryOptions
	// Template is the validated is_template backend Options cloned per member
	Template *bo.Options
	// Conf is the active configuration (middleware assembly for member routes)
	Conf *config.Config
	// Factories is the backend provider registry
	Factories rt.Lookup
	// Caches is the active cache lookup
	Caches cache.Lookup
	// Tracers is the active tracer lookup
	Tracers tracing.Tracers
	// HealthChecker receives member health check registrations
	HealthChecker healthcheck.HealthChecker
	// KnownStatuses optionally seeds member health from a prior instance
	// (config reload), keyed by generated backend name
	KnownStatuses healthcheck.StatusLookup
	// DrainTimeout is how long after removal a member's idle upstream
	// connections are kept before closing (in-flight request drain)
	DrainTimeout time.Duration
}

// externalRegistrar is implemented by health checkers that accept
// caller-managed statuses (provider-readiness health mode)
type externalRegistrar interface {
	RegisterExternal(name, description string, s *healthcheck.Status)
}

// protocolHealthProber mirrors the unexported interface consulted by
// backends.StartHealthChecks for protocol-native probes (e.g. mysql)
type protocolHealthProber interface {
	HealthCheckProbe() healthcheck.Probe
}

// memberEntry is one live discovered member
type memberEntry struct {
	member discovery.Member
	client backends.Backend
	status *healthcheck.Status
	target *pool.Target
	// external is true when status is provider-readiness-driven
	// rather than probe-driven
	external bool
}

// Manager owns the discovered member set of one discovery-backed ALB
type Manager struct {
	cfg     Config
	albName string
	// discoverer is the bound discoverer's name, for metrics/log labels
	discoverer string
	// tracer is the ALB's configured tracer (nil when tracing is not
	// configured); it spans the reconcile cycle, never the request path
	tracer *tracing.Tracer

	mtx     sync.Mutex
	members map[string]*memberEntry
	// applied is the canonical form of the last successfully applied
	// snapshot; nil forces the next snapshot to apply even if identical
	applied discovery.Snapshot
	// lastApply is when the pool was last updated, for debounce pacing
	lastApply time.Time
	// pending holds the newest snapshot deferred by the debounce window
	pending    discovery.Snapshot
	hasPending bool
	timer      *time.Timer
	stopped    bool
}

// New returns a new Manager for the provided Config. The Manager is inert
// until ApplySnapshot is called (typically from a discoverer subscription).
func New(cfg Config) *Manager {
	m := &Manager{
		cfg:        cfg,
		albName:    cfg.ALB.Name(),
		discoverer: cfg.Options.DiscovererName,
		members:    make(map[string]*memberEntry),
	}
	if albCfg := cfg.ALB.Configuration(); albCfg != nil &&
		albCfg.TracingConfigName != "" {
		m.tracer = cfg.Tracers[albCfg.TracingConfigName]
	}
	return m
}

// ApplySnapshot ingests a full-membership snapshot from the discoverer. It
// is safe for concurrent use; snapshots arriving within the configured
// debounce window are coalesced, with only the newest applied.
func (m *Manager) ApplySnapshot(s discovery.Snapshot) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if m.stopped {
		return
	}
	canonical := s.Canonical()
	window := time.Duration(m.cfg.Options.DebounceWindow)
	if window > 0 {
		if elapsed := time.Since(m.lastApply); elapsed < window {
			// within the damping window: hold the newest snapshot and arm
			// (or reuse) a trailing-edge timer for the window remainder
			m.pending = canonical
			if !m.hasPending {
				m.hasPending = true
				m.timer = time.AfterFunc(window-elapsed, m.applyPending)
			}
			return
		}
	}
	m.applyLocked(canonical)
}

// applyPending fires from the debounce timer to apply the newest held
// snapshot
func (m *Manager) applyPending() {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if m.stopped || !m.hasPending {
		return
	}
	s := m.pending
	m.pending = nil
	m.hasPending = false
	m.applyLocked(s)
}

// applyLocked diffs the canonical snapshot against current membership and
// applies it, spanning the full reconcile cycle (snapshot -> diff -> apply)
// when the ALB has a tracer configured. Callers must hold m.mtx.
func (m *Manager) applyLocked(canonical discovery.Snapshot) {
	m.lastApply = time.Now()
	span := m.startReconcileSpan(len(canonical))
	result := resultApplied
	adds, removes := 0, 0
	defer func() {
		metrics.ALBDiscoverySnapshots.WithLabelValues(
			m.albName, m.discoverer, result).Inc()
		if result != resultRejected {
			metrics.ALBDiscoveryLastRefresh.WithLabelValues(
				m.albName, m.discoverer).Set(float64(time.Now().Unix()))
		}
		m.endReconcileSpan(span, result, adds, removes)
	}()
	// guardrail: reject a suspicious shrink below the configured floor and
	// keep serving the last-good membership
	if mm := m.cfg.Options.MinMembers; mm > 0 && len(canonical) < mm {
		result = resultRejected
		discovery.LogWarn("alb discovery snapshot rejected by min_members floor; keeping last-good pool",
			logging.Pairs{
				"albName":     m.albName,
				"snapshot":    len(canonical),
				"min_members": mm,
				"current":     len(m.members),
			})
		return
	}
	if m.applied != nil && canonical.Equal(m.applied) {
		result = resultUnchanged
		return
	}

	assigned := canonical.BackendNames(m.albName)
	removed := make(map[string]*memberEntry)
	incomplete := false

	// pass 1: tear-down list = names absent from the new assignment, plus
	// members whose identity (origin) or replica group changed under the
	// same name (targets capture the group immutably, so a regroup is a
	// rebuild); they are unlinked now so pass 2 can reuse a vacated name
	// for a rebuilt member
	for name, e := range m.members {
		nm, ok := assigned[name]
		if !ok || nm.Key() != e.member.Key() ||
			nm.ReplicaGroup != e.member.ReplicaGroup {
			removed[name] = e
			delete(m.members, name)
		}
	}
	// pass 2: instantiate added members; update changed ones in place
	for name, member := range assigned {
		if e, ok := m.members[name]; ok && member.Key() == e.member.Key() {
			m.updateMember(e, member)
			continue
		}
		e, err := m.instantiateMember(name, member)
		if err != nil {
			discovery.LogError("alb discovery member instantiation failed",
				logging.Pairs{
					"albName": m.albName, "member": name,
					"origin": discovery.SanitizeURL(member.URL()),
					"error":  err.Error(),
				})
			incomplete = true
			continue
		}
		m.members[name] = e
		adds++
		metrics.ALBDiscoveryMemberChanges.WithLabelValues(
			m.albName, m.discoverer, "add").Inc()
		discovery.LogInfo("alb discovery member added", logging.Pairs{
			"albName": m.albName, "member": name,
			"origin": discovery.SanitizeURL(member.URL()),
		})
	}

	// swap the pool before releasing removed members so requests never see
	// a pool referencing a torn-down member
	m.swapPoolLocked()

	for name, e := range removed {
		if _, replaced := m.members[name]; replaced {
			// same-name rebuild (member identity changed): the successor
			// owns the name's health registration and metric series, so
			// only the old client's connections are released
			m.releaseMemberConns(name, e)
		} else {
			m.teardownMember(name, e)
		}
		removes++
		metrics.ALBDiscoveryMemberChanges.WithLabelValues(
			m.albName, m.discoverer, "remove").Inc()
		discovery.LogInfo("alb discovery member removed", logging.Pairs{
			"albName": m.albName, "member": name,
			"origin": discovery.SanitizeURL(e.member.URL()),
		})
	}

	metrics.ALBDiscoveryMembers.WithLabelValues(
		m.albName, m.discoverer).Set(float64(len(m.members)))
	m.debugLogMembership()

	if incomplete {
		result = resultPartial
		// leave applied unset so an identical follow-up snapshot retries
		// the failed instantiations
		m.applied = nil
		return
	}
	m.applied = canonical
}

// debugLogMembership logs the full current membership at debug
func (m *Manager) debugLogMembership() {
	names := make([]string, 0, len(m.members))
	for name, e := range m.members {
		names = append(names,
			name+"="+discovery.SanitizeURL(e.member.URL()))
	}
	slices.Sort(names)
	discovery.LogDebug("alb discovery membership", logging.Pairs{
		"albName":    m.albName,
		"discoverer": m.discoverer,
		"members":    strings.Join(names, " "),
	})
}

// snapshot-processing results, mirrored in the
// trickster_alb_discovery_snapshots_total result label
const (
	resultApplied   = "applied"
	resultUnchanged = "unchanged"
	resultRejected  = "rejected"
	resultPartial   = "partial"
)

// startReconcileSpan opens a span over the reconcile cycle when the ALB
// has a tracer configured; returns nil otherwise. Reconciliation runs on
// the discovery control plane, never on the request hot path.
func (m *Manager) startReconcileSpan(snapshotSize int) trace.Span {
	if m.tracer == nil || m.tracer.Tracer == nil {
		return nil
	}
	_, span := m.tracer.Start(context.Background(), "alb.discovery.reconcile",
		trace.WithAttributes(
			attribute.String("alb.name", m.albName),
			attribute.String("discovery.discoverer", m.discoverer),
			attribute.Int("discovery.snapshot.members", snapshotSize),
		))
	return span
}

func (m *Manager) endReconcileSpan(span trace.Span, result string,
	adds, removes int,
) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.String("discovery.result", result),
		attribute.Int("discovery.members.added", adds),
		attribute.Int("discovery.members.removed", removes),
	)
	span.End()
}

// swapPoolLocked pushes the current member set into the ALB pool as
// deterministic, name-sorted targets
func (m *Manager) swapPoolLocked() {
	names := make([]string, 0, len(m.members))
	for name := range m.members {
		names = append(names, name)
	}
	slices.Sort(names)
	targets := make(pool.Targets, 0, len(names))
	for _, name := range names {
		targets = append(targets, m.members[name].target)
	}
	if !m.cfg.ALB.SetDynamicTargets(targets) {
		discovery.LogDebug("alb pool is stopped; discovery update discarded",
			logging.Pairs{"albName": m.albName})
	}
}

// instantiateMember creates a full runtime backend for a discovered member:
// template clone, provider client (HTTP client, TLS client config), path
// routes with the standard middleware chain, and health monitoring per the
// configured health_mode.
func (m *Manager) instantiateMember(name string, member discovery.Member) (*memberEntry, error) {
	nb, err := template.Instantiate(name, m.cfg.Template, member)
	if err != nil {
		return nil, err
	}
	var c cache.Cache
	if !providers.NonCacheBackends().Contains(nb.Provider) {
		var ok bool
		if c, ok = m.cfg.Caches[nb.CacheName]; !ok {
			return nil, fmt.Errorf("could not find cache named [%s]", nb.CacheName)
		}
	}
	factory, ok := m.cfg.Factories[strings.ToLower(nb.Provider)]
	if !ok || factory == nil {
		return nil, fmt.Errorf("could not find factory for provider [%s]", nb.Provider)
	}
	client, err := factory(name, nb, lm.NewRouter(), c, nil, m.cfg.Factories)
	if err != nil {
		return nil, err
	}
	nb.HTTPClient = client.HTTPClient()
	if c != nil {
		client.SetCache(c)
	}
	// register the member's path routes onto its own router; the throwaway
	// frontend router absorbs the listener-facing registrations a pool-only
	// member does not need
	nb.Paths = client.DefaultPathConfigs(nb).Overlay(nb.Paths)
	routing.RegisterPathRoutes(lm.NewRouter(), m.cfg.Conf, client.Handlers(),
		client, nb, c, m.cfg.Tracers)

	e := &memberEntry{member: member, client: client}
	if m.cfg.Options.HealthMode == ao.HealthModeProvider {
		e.external = true
		e.status = healthcheck.NewStatus(name, m.healthDescription(nb.Provider),
			"", statusForReadyState(member.Ready), time.Time{}, nil)
		if er, ok := m.cfg.HealthChecker.(externalRegistrar); ok {
			er.RegisterExternal(name, m.healthDescription(nb.Provider), e.status)
		}
	} else if nb.HealthCheck != nil {
		// mirror backends.StartHealthChecks: overlay the provider default
		// healthcheck config, then register an active probe
		hco := nb.HealthCheck
		nb.HealthCheck = client.DefaultHealthCheckConfig()
		if nb.HealthCheck == nil {
			nb.HealthCheck = hco
		} else {
			nb.HealthCheck.Overlay(hco)
		}
		var st *healthcheck.Status
		if prober, ok := client.(protocolHealthProber); ok {
			registrar, rok := m.cfg.HealthChecker.(healthcheck.Registrar)
			if !rok {
				return nil, errors.New("health checker does not support protocol probes")
			}
			st, err = registrar.RegisterProbe(name,
				m.healthDescription(nb.Provider), nb.HealthCheck,
				prober.HealthCheckProbe())
		} else {
			st, err = m.cfg.HealthChecker.Register(name,
				m.healthDescription(nb.Provider),
				nb.HealthCheck, client.HealthCheckHTTPClient())
		}
		if err != nil {
			return nil, err
		}
		if oldSt, ok := m.cfg.KnownStatuses[name]; ok && oldSt != nil {
			if v := oldSt.Get(); v != healthcheck.StatusInitializing {
				st.Set(v)
			}
		}
		client.SetHealthCheckProbe(st.Prober())
		e.status = st
	}

	e.target = pool.NewWeightedTarget(client.Router(), e.status, client,
		member.Weight)
	if e.external {
		e.target = e.target.WithExternalHealth()
	}
	return e, nil
}

// healthDescription tags a discovered member on the health status page
// with its provider, owning ALB, and discoverer (plan step 27), so live
// membership reads the same way static backends do
func (m *Manager) healthDescription(provider string) string {
	return fmt.Sprintf("%s (%s via %s)", provider, m.albName, m.discoverer)
}

// updateMember applies attribute-only changes (weight, readiness, labels)
// to a live member without rebuilding its backend client
func (m *Manager) updateMember(e *memberEntry, member discovery.Member) {
	if e.external && member.Ready != e.member.Ready {
		e.status.Set(statusForReadyState(member.Ready))
	}
	if member.Weight != e.member.Weight {
		// targets are immutable; rebuild this member's target around the
		// same client and status
		e.target = pool.NewWeightedTarget(e.client.Router(), e.status,
			e.client, member.Weight)
		if e.external {
			e.target = e.target.WithExternalHealth()
		}
	}
	e.member = member
}

// teardownMember releases a removed member: its health check registration
// is removed immediately (no new probes), its metric series are deleted,
// and its idle upstream connections are closed after the drain timeout so
// in-flight requests dispatched to the old pool complete undisturbed.
func (m *Manager) teardownMember(name string, e *memberEntry) {
	if m.cfg.HealthChecker != nil {
		m.cfg.HealthChecker.Unregister(name)
	}
	metrics.DeleteBackendSeries(name)
	m.releaseMemberConns(name, e)
}

// releaseMemberConns closes a retired member client's idle upstream
// connections after the drain timeout, so in-flight requests dispatched via
// the old pool complete undisturbed
func (m *Manager) releaseMemberConns(name string, e *memberEntry) {
	client := e.client
	drain := m.cfg.DrainTimeout
	safego.Go(func(r any, stack []byte) {
		discovery.LogError("alb discovery member teardown panic", logging.Pairs{
			"albName": m.albName, "member": name,
			"panic": r, "stack": string(stack),
		})
	}, func() {
		if drain > 0 {
			time.Sleep(drain)
		}
		closeIdle(client.HTTPClient())
		closeIdle(client.HealthCheckHTTPClient())
	})
}

func closeIdle(c *http.Client) {
	if c == nil {
		return
	}
	type idleCloser interface{ CloseIdleConnections() }
	if ic, ok := c.Transport.(idleCloser); ok {
		ic.CloseIdleConnections()
	}
}

// Stop tears down every discovered member and permanently disables the
// Manager. Called on shutdown and on config reload (a reload constructs a
// new Manager, optionally seeded from this one's AppliedSnapshot).
func (m *Manager) Stop() {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	if m.timer != nil {
		m.timer.Stop()
	}
	m.pending = nil
	m.hasPending = false
	for name, e := range m.members {
		m.teardownMember(name, e)
	}
	m.members = make(map[string]*memberEntry)
	metrics.DeleteALBDiscoverySeries(m.albName)
}

// AppliedSnapshot returns the canonical form of the last fully-applied
// membership, for seeding a successor Manager across a config reload; nil
// when no snapshot has been fully applied
func (m *Manager) AppliedSnapshot() discovery.Snapshot {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if m.applied == nil {
		return nil
	}
	return m.applied.Clone()
}

// MemberNames returns the generated backend names of the current members,
// sorted
func (m *Manager) MemberNames() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	names := make([]string, 0, len(m.members))
	for name := range m.members {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// statusForReadyState maps provider-reported readiness onto health check
// status values: ready members are Passing, not-ready and terminating
// members are Failing, and readiness-unknown members are Unchecked (see the
// healthy_floor interaction notes on options.HealthModeProvider)
func statusForReadyState(r discovery.ReadyState) int32 {
	switch r {
	case discovery.Ready:
		return healthcheck.StatusPassing
	case discovery.NotReady, discovery.Terminating:
		return healthcheck.StatusFailing
	}
	return healthcheck.StatusUnchecked
}
