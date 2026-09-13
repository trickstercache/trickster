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

package instance

import (
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/dynamic"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/monitor"
)

type ServerInstance struct {
	Config        *config.Config
	Caches        cache.Lookup
	HealthChecker healthcheck.HealthChecker
	Backends      backends.Backends
	Listeners     *listener.Group
	// Readiness is flipped to draining on shutdown so the readiness handler
	// reports not-ready before listeners stop accepting.
	Readiness        *ready.State
	OnConfigReloaded func(*config.Config)
	// OverlayProvider, when set, supplies the in-memory configuration overlay
	// that every reload merges on top of the file-sourced configuration.
	OverlayProvider config.OverlayProvider
	// Reloader triggers a configuration reload through the daemon's single
	// reload path, for in-process producers such as an overlay provider.
	Reloader reload.Reloader
	// mgmtOptions is the last applied management config, published atomically
	// so shutdown can read its settings while a reload may be committing.
	mgmtOptions atomic.Pointer[mgmt.Options]
	// Discoverers holds the running autodiscovery provider instances,
	// keyed by discoverer name; rebuilt on each config (re)load
	Discoverers map[string]discovery.Discoverer
	// PoolManagers holds the dynamic pool manager for each
	// discovery-backed ALB, keyed by ALB backend name
	PoolManagers map[string]*dynamic.Manager
	CertMonitor  *monitor.Monitor
	// Tracers holds the tracers the applied configuration registered, by name
	Tracers tracing.Tracers
}

// SetMgmtOptions publishes the management options of a newly applied config.
func (si *ServerInstance) SetMgmtOptions(o *mgmt.Options) {
	if si != nil && o != nil {
		si.mgmtOptions.Store(o.Clone())
	}
}

// MgmtOptions returns the last applied management options, or defaults.
func (si *ServerInstance) MgmtOptions() *mgmt.Options {
	if si != nil {
		if o := si.mgmtOptions.Load(); o != nil {
			return o
		}
	}
	return mgmt.New()
}
