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

package alb

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/registry"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	authopt "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	authreg "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	at "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/local"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Client Implements the Backend Interface
type Client struct {
	backends.Backend
	handler types.Mechanism // this is the actual handler for all request to this backend

	// poolMtx serializes pool swaps (startup, discovery updates) against
	// StopPool; the request path never takes it -- mechanisms read the pool
	// via an atomic pointer
	poolMtx sync.Mutex
	// staticTargets is the configured (non-discovered) member set, built
	// once at ValidateAndStartPool
	staticTargets pool.Targets
	// poolStopped suppresses swaps after StopPool so a late discovery
	// update cannot resurrect goroutines during shutdown/reload
	poolStopped bool
	// floorWasReset dedupes the healthy_floor-reset warning across
	// repeated swaps of a churning discovered membership
	floorWasReset bool
	// dynamicNames is the current discovered member-name list, for
	// health/mgmt display
	dynamicNames atomic.Pointer[[]string]
}

// Handlers returns a map of the HTTP Handlers the client has registered.
// "localresponse" is exposed so operators can use the standard `paths:`
// override to short-circuit non-mergeable endpoints (e.g. /api/v1/query_exemplars,
// /api/v1/metadata) before they enter the ALB mechanism. Without this entry
// the routing layer silently drops the path config because client.Handlers()
// has no matching handler for the requested name.
func (c *Client) Handlers() handlers.Lookup {
	return handlers.Lookup{
		providers.ALB:   c.handler,
		"localresponse": http.HandlerFunc(local.HandleLocalResponse),
	}
}

var _ rt.NewBackendClientFunc = NewClient

// NewClient returns a new ALB client reference
func NewClient(name string, o *bo.Options, router http.Handler,
	_ cache.Cache, _ backends.Backends, factories rt.Lookup,
) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.New(name, o, nil, router, nil)
	if err != nil {
		return nil, err
	}
	c.Backend = b
	if o != nil && o.ALBOptions != nil {
		if o.ALBOptions.MaxCaptureBytes == 0 {
			o.ALBOptions.MaxCaptureBytes = o.MaxCaptureBytes
		}
		if o.ALBOptions.MaxFanoutCaptureBytes == 0 {
			o.ALBOptions.MaxFanoutCaptureBytes = o.MaxFanoutCaptureBytes
		}
		m, err := registry.New(o.ALBOptions.MechanismName,
			o.ALBOptions, factories)
		if err != nil {
			return nil, err
		}
		c.handler = m
	}
	return c, nil
}

// StartALBPools ensures that ALB's are fully loaded, which can't be done
// until all backends are processed, so the ALB's destination backend names
// can be mapped to their respective clients
func StartALBPools(clients backends.Backends, hcs healthcheck.StatusLookup) error {
	for _, c := range clients {
		if rc, ok := c.(*Client); ok {
			err := rc.ValidateAndStartPool(clients, hcs)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// StopPools ensures that ALBs are fully stopped when the process's
// configuration is reloaded
func StopPools(clients backends.Backends) error {
	for _, c := range clients {
		if rc, ok := c.(*Client); ok {
			rc.StopPool()
		}
	}
	return nil
}

// ValidateClients iterates the backends and validates ALB backends
func ValidateClients(clients backends.Backends) error {
	backendNames := sets.MapKeysToStringSet(clients)
	nestedTSMMembers := sets.NewStringSet()
	for _, client := range clients {
		if client == nil || client.Configuration() == nil {
			continue
		}
		cfg := client.Configuration()
		if cfg.Provider == providers.ALB && cfg.ALBOptions != nil &&
			cfg.ALBOptions.MechanismName == names.MechanismTSM {
			for _, member := range cfg.ALBOptions.Pool {
				nestedTSMMembers.Set(member.Name)
			}
		}
	}
	for _, v := range clients {
		if v == nil || v.Configuration().Provider != providers.ALB {
			continue
		}
		cfg := v.Configuration()
		if cfg.ReplicaGroup != "" && cfg.ReplicaGroup != cfg.Name &&
			!nestedTSMMembers.Contains(cfg.Name) {
			return fmt.Errorf("replica_group on ALB backend %q is only valid when it is a direct TSM pool member",
				cfg.Name)
		}
		if c, ok := v.(*Client); ok {
			err := c.Validate(backendNames)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidatePool confirms the provided list of backends is valid
func (c *Client) Validate(backends sets.Set[string]) error {
	o := c.Configuration()
	if o.ALBOptions == nil {
		return errors.ErrInvalidOptions
	}
	if !registry.IsRegistered(o.ALBOptions.MechanismName) {
		return fmt.Errorf("invalid mechanism name [%s] in backend [%s]",
			o.ALBOptions.MechanismName, o.Name)
	}
	return c.ValidatePool(backends)
}

// ValidatePool confirms the provided list of backends is valid
func (c *Client) ValidatePool(backends sets.Set[string]) error {
	o := c.Configuration().ALBOptions
	if o == nil {
		return errors.ErrInvalidOptions
	}
	return o.ValidatePool(c.Name(), backends)
}

// ValidateAndStartPool starts this Client's pool up using the provided list of
// backends to validate and map out the pool configuration
func (c *Client) ValidateAndStartPool(clients backends.Backends, hcs healthcheck.StatusLookup) error {
	if c.Configuration() == nil || c.Configuration().ALBOptions == nil {
		return errors.ErrInvalidOptions
	}
	o := c.Configuration().ALBOptions
	err := c.ValidatePool(sets.MapKeysToStringSet(clients))
	if err != nil {
		return err
	}
	if o.MechanismName == names.MechanismUR && o.UserRouter != nil {
		return c.validateAndStartUserRouter(clients, hcs)
	}
	targets := make(pool.Targets, 0, len(o.Pool))
	for _, m := range o.Pool {
		tc, ok := clients[m.Name]
		if !ok {
			return alberr.NewErrInvalidPoolMemberName(c.Name(), m.Name)
		}
		if o.MechanismName == names.MechanismTSM {
			if err := validateTSMPoolMemberProvider(m.Name, clients, sets.NewStringSet()); err != nil {
				return err
			}
		}
		hc, ok := hcs[m.Name]
		if !ok {
			// virtual backends (rule, alb) have no health checks; treat as passing
			hc = healthcheck.NewStatus(m.Name, "virtual", "", healthcheck.StatusPassing, time.Time{}, nil)
		}
		targets = append(targets,
			pool.NewWeightedTarget(tc.Router(), hc, tc, m.EffectiveWeight()))
	}
	c.poolMtx.Lock()
	c.staticTargets = targets
	c.swapPool(targets)
	c.poolMtx.Unlock()
	if o.HealthyFloor <= int(healthcheck.StatusFailing) {
		// floor admits members whose probe has confirmed them down; operators
		// who lowered the floor to keep traffic flowing during the startup
		// Initializing window may not realize Failing slips in too.
		metrics.ALBPoolAdmitsFailing.WithLabelValues(c.Name()).Set(1)
		logger.Warn("alb healthy_floor admits members in Failing state",
			logging.Pairs{
				"backend_name":  c.Name(),
				"healthy_floor": o.HealthyFloor,
				"hint":          "set healthy_floor: 0 to exclude probed-failing members",
			})
	} else {
		metrics.ALBPoolAdmitsFailing.WithLabelValues(c.Name()).Set(0)
	}
	return nil
}

// swapPool builds a new Pool from the provided full target set and installs
// it on the mechanism via its atomic holder, then stops the superseded pool.
// The request hot path is untouched: dispatchers load the pool through an
// atomic pointer, and in-flight requests holding the old pool's targets
// complete normally (target handlers and statuses outlive the pool object).
// Callers must hold c.poolMtx.
func (c *Client) swapPool(targets pool.Targets) {
	if c.poolStopped {
		return
	}
	pm, ok := c.handler.(types.PoolMechanism)
	if !ok {
		return
	}
	oldPool := pm.Pool()
	pm.SetPool(pool.New(targets, c.effectiveFloor(targets)))
	if oldPool != nil {
		oldPool.Stop()
	}
}

// effectiveFloor returns the healthy floor to enforce for the provided
// membership. When the configured floor requires Passing but the set
// includes unprobed members (no health check interval, and no external
// health source), those members could never be admitted -- potentially
// emptying the pool and 502ing every request -- so the floor is reset to 0
// and the condition is surfaced loudly.
func (c *Client) effectiveFloor(targets pool.Targets) int {
	o := c.Configuration().ALBOptions
	var unprobed []string
	for _, t := range targets {
		if t != nil && !t.Probed() {
			unprobed = append(unprobed, t.Name())
		}
	}
	if o.HealthyFloor >= int(healthcheck.StatusPassing) && len(unprobed) > 0 {
		metrics.ALBPoolFloorReset.WithLabelValues(c.Name()).Set(1)
		if !c.floorWasReset {
			c.floorWasReset = true
			logger.Warn("alb healthy_floor reset to 0: pool members have no health check",
				logging.Pairs{
					"backend_name":  c.Name(),
					"healthy_floor": o.HealthyFloor,
					"members":       strings.Join(unprobed, ","),
					"hint":          "configure healthcheck.interval on these members, or set healthy_floor: 0",
				})
		}
		return int(healthcheck.StatusUnchecked)
	}
	c.floorWasReset = false
	metrics.ALBPoolFloorReset.WithLabelValues(c.Name()).Set(0)
	return o.HealthyFloor
}

// SetDynamicTargets atomically replaces the ALB's discovered member set at
// runtime. The new pool is the configured static members plus the provided
// dynamic targets; no listener restart, cache teardown, or config reload is
// involved. It returns false if the pool has been stopped (shutdown/reload
// in progress), in which case the update was discarded.
func (c *Client) SetDynamicTargets(dynamic pool.Targets) bool {
	c.poolMtx.Lock()
	defer c.poolMtx.Unlock()
	if c.poolStopped {
		return false
	}
	combined := make(pool.Targets, 0, len(c.staticTargets)+len(dynamic))
	combined = append(combined, c.staticTargets...)
	combined = append(combined, dynamic...)
	c.swapPool(combined)
	names := make([]string, len(dynamic))
	for i, t := range dynamic {
		if t != nil {
			names[i] = t.Name()
		}
	}
	c.dynamicNames.Store(&names)
	return true
}

// DynamicPoolNames returns the names of the ALB's currently-discovered pool
// members, for health and management display
func (c *Client) DynamicPoolNames() []string {
	if n := c.dynamicNames.Load(); n != nil {
		return slices.Clone(*n)
	}
	return nil
}

// validateTSMPoolMemberProvider resolves virtual ALB members to their terminal
// pool leaves. A nested ALB is compatible with TSM when every leaf produces a
// supported time-series format; checking only the immediate provider would
// incorrectly reject topologies such as TSM -> round-robin ALB -> Prometheus.
func validateTSMPoolMemberProvider(name string, clients backends.Backends,
	visited sets.Set[string],
) error {
	if visited.Contains(name) {
		return fmt.Errorf("%w: cycle encountered at backend %q",
			alberr.ErrInvalidTimeSeriesMergeProvider, name)
	}
	client, ok := clients[name]
	if !ok || client == nil || client.Configuration() == nil {
		return alberr.NewErrInvalidPoolMemberName("", name)
	}
	cfg := client.Configuration()
	if providers.IsSupportedTimeSeriesMergeProvider(cfg.Provider) {
		return nil
	}
	if cfg.Provider != providers.ALB || cfg.ALBOptions == nil ||
		len(cfg.ALBOptions.Pool) == 0 {
		return fmt.Errorf("%w: backend %q uses provider %q",
			alberr.ErrInvalidTimeSeriesMergeProvider, name, cfg.Provider)
	}
	nextVisited := visited.Clone()
	nextVisited.Set(name)
	for _, child := range cfg.ALBOptions.Pool {
		if err := validateTSMPoolMemberProvider(child.Name, clients, nextVisited); err != nil {
			return err
		}
	}
	return nil
}

// PlanTSMMerge delegates planning through a virtual ALB wrapper to its
// configured-first terminal TSM provider. This lets an outer TSM use nested
// mechanisms such as round-robin without losing provider-specific PromQL
// planning.
func (c *Client) PlanTSMMerge(r *http.Request, query string) (*tsmerge.TSMMergePlan, error) {
	backend, err := c.terminalTSMBackend(sets.NewStringSet())
	if err != nil {
		return nil, err
	}
	planner, ok := backend.(backends.TSMMergeProvider)
	if !ok {
		return nil, fmt.Errorf("%w: backend %q does not provide a merge planner",
			alberr.ErrInvalidTimeSeriesMergeProvider, backend.Name())
	}
	return planner.PlanTSMMerge(r, query)
}

// FinalizeTSMMerge delegates provider-specific finalization through nested ALB
// wrappers. The selected terminal backend is the same one used for planning.
func (c *Client) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	backend, err := c.terminalTSMBackend(sets.NewStringSet())
	if err != nil {
		return
	}
	if finalizer, ok := backend.(interface {
		FinalizeTSMMerge(string, timeseries.Timeseries)
	}); ok {
		finalizer.FinalizeTSMMerge(query, ts)
	}
}

// TSMInjectedLabelKeys returns the union of labels injected by every terminal
// time-series backend beneath this ALB wrapper.
func (c *Client) TSMInjectedLabelKeys() []string {
	seen := make(map[string]struct{})
	c.collectTSMInjectedLabelKeys(sets.NewStringSet(), seen)
	keys := sets.MapKeysToStringSet(seen).Keys()
	slices.Sort(keys)
	return keys
}

func (c *Client) collectTSMInjectedLabelKeys(visited sets.Set[string],
	seen map[string]struct{},
) {
	if c == nil || c.Configuration() == nil || visited.Contains(c.Name()) {
		return
	}
	nextVisited := visited.Clone()
	nextVisited.Set(c.Name())
	pm, ok := c.handler.(types.PoolMechanism)
	if !ok || pm.Pool() == nil {
		return
	}
	for _, target := range pm.Pool().ConfiguredTargets() {
		if target == nil || target.Backend() == nil {
			continue
		}
		backend := target.Backend()
		if nested, ok := backend.(*Client); ok {
			nested.collectTSMInjectedLabelKeys(nextVisited, seen)
			continue
		}
		cfg := backend.Configuration()
		if cfg == nil || cfg.Prometheus == nil {
			continue
		}
		for key := range cfg.Prometheus.Labels {
			seen[key] = struct{}{}
		}
	}
}

func (c *Client) terminalTSMBackend(visited sets.Set[string]) (backends.Backend, error) {
	if c == nil || c.Configuration() == nil {
		return nil, fmt.Errorf("%w: nested ALB is not configured",
			alberr.ErrInvalidTimeSeriesMergeProvider)
	}
	name := c.Name()
	if visited.Contains(name) {
		return nil, fmt.Errorf("%w: cycle encountered at backend %q",
			alberr.ErrInvalidTimeSeriesMergeProvider, name)
	}
	nextVisited := visited.Clone()
	nextVisited.Set(name)

	pm, ok := c.handler.(types.PoolMechanism)
	if !ok || pm.Pool() == nil {
		return nil, fmt.Errorf("%w: nested ALB %q has no available pool",
			alberr.ErrInvalidTimeSeriesMergeProvider, name)
	}
	for _, target := range pm.Pool().ConfiguredTargets() {
		if target == nil || target.Backend() == nil {
			continue
		}
		backend := target.Backend()
		if nested, ok := backend.(*Client); ok {
			return nested.terminalTSMBackend(nextVisited)
		}
		if _, ok := backend.(backends.TSMMergeProvider); ok {
			return backend, nil
		}
	}
	return nil, fmt.Errorf("%w: nested ALB %q has no terminal merge provider",
		alberr.ErrInvalidTimeSeriesMergeProvider, name)
}

func observeOnlyOpts() *authopt.Options {
	return &authopt.Options{ObserveOnly: true}
}

func (c *Client) validateAndStartUserRouter(clients backends.Backends, hcs healthcheck.StatusLookup) error {
	conf := c.Configuration()
	var canReplaceCreds bool
	var authenticator at.Authenticator
	var defaultHandler http.Handler
	var defaultTarget backends.RouteTarget
	o := conf.ALBOptions.UserRouter
	h, ok := c.handler.(*ur.Handler)
	if !ok {
		return nil
	}
	if conf.AuthOptions != nil && conf.AuthOptions.Authenticator != nil {
		// credential replacement is only allowed if users will be positively
		// authenticated and not just observed.
		canReplaceCreds = !(conf.AuthOptions.Authenticator.IsObserveOnly())
		authenticator = conf.AuthOptions.Authenticator
	} else {
		a, err := authreg.NewObserverFromProviderName(o.TargetProvider,
			map[string]any{"options": observeOnlyOpts()})
		if err != nil {
			return err
		} else if a == nil {
			return errors.ErrInvalidOptions
		}
		authenticator = a
	}
	noRouteStatusCode := o.NoRouteStatusCode
	if o.DefaultBackend != "" {
		bh, ok := clients[o.DefaultBackend]
		if !ok || bh == nil {
			return alberr.NewErrInvalidBackendName(c.Name(), o.DefaultBackend)
		}
		defaultTarget = backends.RouteTarget{Backend: bh}
	} else {
		if noRouteStatusCode < http.StatusBadRequest || noRouteStatusCode >= 600 {
			noRouteStatusCode = http.StatusBadGateway
		}
		switch noRouteStatusCode {
		case http.StatusUnauthorized:
			defaultHandler = http.HandlerFunc(failures.HandleUnauthorized)
		case http.StatusBadGateway:
			defaultHandler = http.HandlerFunc(failures.HandleBadGateway)
		case http.StatusBadRequest:
			defaultHandler = http.HandlerFunc(failures.HandleBadRequestResponse)
		case http.StatusInternalServerError:
			defaultHandler = http.HandlerFunc(failures.HandleInternalServerError)
		case http.StatusNotFound:
			defaultHandler = http.HandlerFunc(failures.HandleNotFound)
		default:
			defaultHandler = http.HandlerFunc(func(w http.ResponseWriter,
				_ *http.Request,
			) {
				failures.HandleMiscFailure(noRouteStatusCode, w)
			})
		}
	}

	routes := make(ur.UserRoutes, len(o.Users))
	for username, m := range o.Users {
		if m.ToBackend != "" {
			bh, ok := clients[m.ToBackend]
			if !ok || bh == nil {
				return alberr.NewErrInvalidBackendName(c.Name(), m.ToBackend)
			}
			route := ur.UserRoute{Backend: bh}
			if hc, ok := hcs[m.ToBackend]; ok {
				route.Status = hc
			}
			routes[username] = route
		}
		if !canReplaceCreds && m.ToCredential != "" {
			return alberr.NewErrInvalidUserRouterCreds(c.Name())
		}
	}

	h.SetAuthenticator(authenticator, canReplaceCreds)
	h.SetRouterName(c.Name())
	h.SetDefaultTarget(defaultTarget)
	h.SetDefaultHandler(defaultHandler)
	h.SetNoRouteStatusCode(noRouteStatusCode)
	h.SetUserRoutes(routes)

	return nil
}

// RouteResolver returns the protocol-neutral resolver implemented by a User
// Router ALB. Other ALB mechanisms do not select routes by authenticated user.
func (c *Client) RouteResolver() backends.RouteResolver {
	if h, ok := c.handler.(backends.RouteResolver); ok {
		return h
	}
	return nil
}

// StopPool stops this Client's pool and permanently rejects further swaps
// on this Client (a new Client is built on reload). No-op for handlers that
// don't own a pool (e.g. user_router).
func (c *Client) StopPool() {
	c.poolMtx.Lock()
	c.poolStopped = true
	c.poolMtx.Unlock()
	if pm, ok := c.handler.(types.PoolMechanism); ok {
		pm.StopPool()
	}
}

// Boilerplate Interface Functions (to EOF)

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.List {
	return po.List{
		{
			Path:          "/",
			HandlerName:   providers.ALB,
			Methods:       methods.AllHTTPMethods(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: matching.PathMatchNamePrefix,
		},
	}
}
