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
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

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
		if IsVirtual(bo.Provider) {
			// Virtual backends have no upstream to probe; register a synthetic
			// passing status so they surface in the health page and in outer
			// ALB pool reporting.
			hc.RegisterVirtual(k, bo.Provider)
			continue
		}
		hco := bo.HealthCheck
		def := c.DefaultHealthCheckConfig()
		if hco == nil && def == nil {
			// no probe possible for this provider; the ALB pool will reject
			// this backend as a non-virtual member with no Status.
			continue
		}
		if def != nil {
			bo.HealthCheck = def
			if hco != nil {
				bo.HealthCheck.Overlay(hco)
			}
		} else {
			bo.HealthCheck = hco
		}
		var autoApplied bool
		if bo.HealthCheck.Interval <= 0 {
			// The operator did not configure a probe interval. A registered
			// target with Interval == 0 never ticks, so its Status sticks at
			// StatusUnchecked and the ALB pool's dispatch-time filter keeps
			// routing fanout traffic regardless of upstream health. Apply a
			// fast auto-default and surface that we did so.
			bo.HealthCheck.Interval = ho.DefaultAutoProbeInterval
			if bo.HealthCheck.FailureThreshold <= 0 {
				bo.HealthCheck.FailureThreshold = 1
			}
			if bo.HealthCheck.RecoveryThreshold <= 0 {
				bo.HealthCheck.RecoveryThreshold = 1
			}
			autoApplied = true
			metrics.BackendsDefaultHealthCheckApplied.WithLabelValues(k, bo.Provider).Inc()
			logger.Warn("auto-applied default healthcheck for backend",
				logging.Pairs{"backend_name": k, "provider": bo.Provider})
		}
		st, err := hc.Register(k, bo.Provider, bo.HealthCheck, c.HealthCheckHTTPClient())
		if err != nil {
			return nil, err
		}
		if oldSt, ok := knownStatuses[k]; ok {
			if v := oldSt.Get(); v != healthcheck.StatusInitializing {
				st.Set(v)
			}
		} else if autoApplied {
			// Override the registration-time Initializing with Unchecked so the
			// pool's dispatch filter accepts the target during the brief window
			// before the first auto-probe completes. Targets the operator
			// explicitly configured keep Initializing so they're held out
			// until the probe confirms them.
			st.Set(healthcheck.StatusUnchecked)
		}
		c.SetHealthCheckProbe(st.Prober())
	}
	return hc, nil
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

// UsesCache returns true if the backend uses a cache
// (anything except Virtuals and ReverseProxy)
func UsesCache(provider string) bool {
	return !IsVirtual(provider) && provider != providers.ReverseProxyShort &&
		provider != providers.ReverseProxy
}
