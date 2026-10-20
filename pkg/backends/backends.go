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

// Package backends the interface and generic functionality for Backend providers
package backends

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

type protocolHealthProber interface {
	HealthCheckProbe() healthcheck.Probe
}

// healthCheckRefuser is implemented by a backend that cannot actively probe some origins, and
// says why; such a backend is left unprobed rather than probed in a way that can only fail
type healthCheckRefuser interface {
	HealthCheckUnsupported() string
}

// healthCheckFinalizer returns the effective options a probe registers with,
// possibly a clone that leaves the input's declarative state untouched.
type healthCheckFinalizer interface {
	FinalizeHealthCheckOptions(*ho.Options) *ho.Options
}

// healthStatusOwner is a virtual backend that may keep a health status of its own, as an ALB
// whose availability follows its pool does; nil when it keeps none.
type healthStatusOwner interface {
	HealthStatus() *healthcheck.Status
}

// externalRegistrar is a health checker that reports a status it does not drive
type externalRegistrar interface {
	RegisterExternal(name, description string, s *healthcheck.Status)
}

func ownStatus(c Backend) *healthcheck.Status {
	if o, ok := c.(healthStatusOwner); ok {
		return o.HealthStatus()
	}
	return nil
}

// Backends represents a map of Backends keyed by Name
type Backends map[string]Backend

// StartHealthChecks iterates the backends to fully configure health checkers
// and start up any intervaled health checks. knownStatuses is optional and
// sets the initial status of the provided targets (e.g., after a config reload)
func (b Backends) StartHealthChecks(knownStatuses healthcheck.StatusLookup) (healthcheck.HealthChecker, error) {
	hc := healthcheck.New()
	for k, c := range b {
		bo := c.Configuration()
		if k == "frontend" {
			continue
		}
		if !HasOrigin(bo.Provider) {
			// No upstream to probe: a backend that keeps its own status reports it, so the
			// health page agrees with routing; any other gets a synthetic passing status.
			if st := ownStatus(c); st != nil {
				if er, ok := hc.(externalRegistrar); ok {
					er.RegisterExternal(k, bo.Provider, st)
					continue
				}
			}
			hc.RegisterVirtual(k, bo.Provider)
			continue
		}
		st, err := RegisterHealthCheck(hc, k, bo.Provider, c)
		if err != nil {
			// the checks registered before this one are already probing
			hc.Shutdown()
			return nil, err
		}
		if st == nil {
			continue
		}
		if oldSt, ok := knownStatuses[k]; ok {
			if v := oldSt.Get(); v != healthcheck.StatusInitializing {
				st.Set(v)
			}
		}
		c.SetHealthCheckProbe(st.Prober())
	}
	return hc, nil
}

// ErrNoProbeRegistrar is returned when a backend probes by protocol and the health checker
// cannot register such a probe.
var ErrNoProbeRegistrar = errors.New("health checker does not support protocol probe registration")

// RegisterHealthCheck registers the active health check of a configured or discovered backend
// and returns its status. The status is nil, with no error, for a backend that configures no
// health check or whose origin cannot be probed; the latter is logged when a check was asked for.
func RegisterHealthCheck(hc healthcheck.HealthChecker, name, description string, c Backend,
) (*healthcheck.Status, error) {
	bo := c.Configuration()
	hco := bo.HealthCheck
	if hco == nil {
		return nil, nil
	}
	bo.HealthCheck = c.DefaultHealthCheckConfig()
	if bo.HealthCheck == nil {
		bo.HealthCheck = hco
	} else {
		bo.HealthCheck.Overlay(hco)
	}
	probeOpts := bo.HealthCheck
	if f, ok := c.(healthCheckFinalizer); ok && probeOpts != nil {
		probeOpts = f.FinalizeHealthCheckOptions(probeOpts)
	}
	if u, ok := c.(healthCheckRefuser); ok {
		if why := u.HealthCheckUnsupported(); why != "" {
			if probeOpts != nil && probeOpts.Interval > 0 {
				logger.Warn("backend health check is not run", logging.Pairs{"backendName": name, "detail": why})
			}
			return nil, nil
		}
	}
	var probe healthcheck.Probe
	if prober, ok := c.(protocolHealthProber); ok {
		// a backend may probe by protocol for some origins and by request for the rest
		probe = prober.HealthCheckProbe()
	}
	if probe == nil {
		return hc.Register(name, description, probeOpts, c.HealthCheckHTTPClient())
	}
	registrar, ok := hc.(healthcheck.Registrar)
	if !ok {
		return nil, ErrNoProbeRegistrar
	}
	return registrar.RegisterProbe(name, description, probeOpts, probe)
}

// Get returns the named origin
func (b Backends) Get(backendName string) Backend {
	if c, ok := b[backendName]; ok {
		return c
	}
	return nil
}

// GetConfig returns the named Backend's Configuration Options
func (b Backends) GetConfig(backendName string) *bo.Options {
	if c, ok := b[backendName]; ok {
		return c.Configuration()
	}
	return nil
}

// GetRouter returns the named Backend's Request Router
func (b Backends) GetRouter(backendName string) http.Handler {
	if c, ok := b[backendName]; ok {
		return c.Router()
	}
	return nil
}

// IsVirtual returns true if the backend is a virtual type (e.g., ones that do not
// make an outbound http request, but instead front to other backends)
func IsVirtual(provider string) bool {
	return provider == providers.ALB || provider == providers.Rule
}

// HasOrigin returns false if the backend never makes an outbound request of
// its own: a virtual type, or one that answers from local content
func HasOrigin(provider string) bool {
	return !IsVirtual(provider) && provider != providers.Static
}

// UpgradeRelay is a virtual backend that may send each request it serves to one backend, whose
// own route then tunnels a protocol upgrade; RelaysUpgrades is false for one that cannot.
type UpgradeRelay interface {
	RelaysUpgrades() bool
}

// RelaysUpgrades reports whether the backend has no origin of its own and sends each request to
// one backend, which can tunnel an upgrade the request asks for.
func RelaysUpgrades(b Backend) bool {
	if b == nil || b.Configuration() == nil || HasOrigin(b.Configuration().Provider) {
		return false
	}
	r, ok := b.(UpgradeRelay)
	return ok && r.RelaysUpgrades()
}

// CloseIdleConnections closes idle keep-alive conns on each backend's web and
// health-check transports. Reload replaces the backend map without closing the
// old map's transports, leaking persistConn readLoop/writeLoop goroutines until
// the per-transport IdleConnTimeout (default 2m) elapses.
func (b Backends) CloseIdleConnections() {
	for _, c := range b {
		if c == nil {
			continue
		}
		closeIdle(c.HTTPClient())
		closeIdle(c.HealthCheckHTTPClient())
	}
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

// Retirer holds what its HTTP transports don't, as a connection of its own or work it runs in the background. Retire,
// called once its requests have drained, releases that and starts no more such work, waiting for it no longer than ctx.
type Retirer interface {
	Retire(ctx context.Context)
}

// Retire retires v when it is a Retirer.
func Retire(ctx context.Context, v any) {
	if r, ok := v.(Retirer); ok {
		r.Retire(ctx)
	}
}

// Retire retires each backend once their requests have drained, waiting no longer than wait in all for the work they
// still run; virtual backends go first, as what they run reads through the others.
func (b Backends) Retire(wait time.Duration) {
	if len(b) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	b.RetireContext(ctx)
}

// RetireContext is Retire, waiting no longer than ctx. The backends of each kind retire together, so none waits out
// another's work, or finds ctx ended by it.
func (b Backends) RetireContext(ctx context.Context) {
	var wg sync.WaitGroup
	for _, virtual := range [2]bool{true, false} {
		for name, c := range b {
			if r, ok := c.(Retirer); ok && isVirtual(c) == virtual {
				wg.Go(func() { safego.Run(retirePanicHandler(name), func() { r.Retire(ctx) }) })
			}
		}
		wg.Wait()
	}
}

func retirePanicHandler(name string) safego.PanicHandler {
	return func(r any, stack []byte) {
		logger.Error("backend retirement panicked",
			logging.Pairs{keys.BackendName: name, keys.Panic: r, keys.Stack: string(stack)})
	}
}

func isVirtual(c Backend) bool {
	o := c.Configuration()
	return o != nil && IsVirtual(o.Provider)
}

// UsesCache returns true if the backend uses a cache
// (anything except Virtuals, Static and ReverseProxy)
func UsesCache(provider string) bool {
	return HasOrigin(provider) && provider != providers.ReverseProxyShort &&
		provider != providers.ReverseProxy
}
