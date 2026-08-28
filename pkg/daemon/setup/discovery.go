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

package setup

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/dynamic"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	dregistry "github.com/trickstercache/trickster/v2/pkg/discovery/registry"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
)

// applyDiscoveryConfig (re)builds the autodiscovery control plane on each
// config (re)load: it stops the previous instance's discoverers and dynamic
// pool managers (capturing their applied membership first), constructs a
// discoverer per referenced entry in the top-level discovery section, and
// creates a dynamic pool manager per discovery-backed ALB, seeded with the
// previous membership when the relevant configuration is unchanged so a
// no-op reload keeps serving the discovered pool without waiting for the
// provider's next snapshot.
func applyDiscoveryConfig(si *instance.ServerInstance, newConf *config.Config,
	clients backends.Backends, caches cache.Lookup, tracers tracing.Tracers,
	knownStatuses healthcheck.StatusLookup,
) error {
	// capture last-good membership from the outgoing managers, then stop
	// the outgoing control plane. (On a failed reload, rollback restores
	// the old config but, as with ALB pools and health checks, the old
	// discovery control plane is not resurrected; the next successful
	// (re)load rebuilds it.)
	seeds := make(map[string]discovery.Snapshot, len(si.PoolManagers))
	for albName, mgr := range si.PoolManagers {
		if mgr == nil {
			continue
		}
		if s := mgr.AppliedSnapshot(); s != nil {
			seeds[albName] = s
		}
		mgr.Stop()
	}
	for name, d := range si.Discoverers {
		if d == nil {
			continue
		}
		if err := d.Stop(); err != nil {
			logger.Warn("error stopping discoverer during reload",
				logging.Pairs{"discoverer": name, "error": err.Error()})
		}
	}
	si.Discoverers = nil
	si.PoolManagers = nil

	// enumerate discovery-backed ALBs
	type discoALB struct {
		client *alb.Client
		opts   *ao.DiscoveryOptions
	}
	albs := make(map[string]discoALB)
	failFast := false
	for name, c := range clients {
		ac, ok := c.(*alb.Client)
		if !ok {
			continue
		}
		cfg := ac.Configuration()
		if cfg == nil || cfg.ALBOptions == nil || cfg.ALBOptions.Discovery == nil {
			continue
		}
		albs[name] = discoALB{client: ac, opts: cfg.ALBOptions.Discovery}
		if cfg.ALBOptions.Discovery.StartupPolicy == ao.StartupPolicyFail {
			failFast = true
		}
	}
	if len(albs) == 0 {
		return nil
	}

	// handleUnavailable applies the startup policy for a discoverer that
	// cannot be brought up: fail startup when any referencing ALB requires
	// it; otherwise serve static members and retry on the next (re)load
	handleUnavailable := func(name string, err error) error {
		if failFast {
			return fmt.Errorf("discoverer %q unavailable: %w", name, err)
		}
		logger.Warn("discoverer unavailable; its albs serve static members only until the next config load",
			logging.Pairs{"discoverer": name, "error": err.Error()})
		return nil
	}

	// build and start one discoverer per referenced discovery entry
	discoverers := make(map[string]discovery.Discoverer)
	for _, a := range albs {
		dn := a.opts.DiscovererName
		if _, ok := discoverers[dn]; ok {
			continue
		}
		d, err := dregistry.New(newConf.Discovery[dn])
		if err != nil {
			if perr := handleUnavailable(dn, err); perr != nil {
				return perr
			}
			continue
		}
		if err = d.Start(context.Background()); err != nil {
			if perr := handleUnavailable(dn, err); perr != nil {
				return perr
			}
			continue
		}
		discoverers[dn] = d
	}

	drain := time.Duration(newConf.MgmtConfig.ReloadDrainTimeout)
	managers := make(map[string]*dynamic.Manager, len(albs))
	for albName, a := range albs {
		tmpl := newConf.Backends[a.opts.TemplateBackend]
		mgr := dynamic.New(dynamic.Config{
			ALB:           a.client,
			Options:       a.opts,
			Template:      tmpl,
			Conf:          newConf,
			Factories:     providerregistry.SupportedProviders(),
			Caches:        caches,
			Tracers:       tracers,
			HealthChecker: si.HealthChecker,
			KnownStatuses: knownStatuses,
			DrainTimeout:  drain,
		})
		if seed, ok := seeds[albName]; ok &&
			discoveryConfigUnchanged(si.Config, newConf, albName, a.opts) {
			mgr.ApplySnapshot(seed)
		}
		if d, ok := discoverers[a.opts.DiscovererName]; ok {
			if _, err := d.Subscribe(a.opts.Query, mgr.ApplySnapshot); err != nil {
				if perr := handleUnavailable(a.opts.DiscovererName, err); perr != nil {
					return perr
				}
			}
		}
		managers[albName] = mgr
	}
	si.Discoverers = discoverers
	si.PoolManagers = managers
	return nil
}

// discoveryConfigUnchanged reports whether the configuration inputs that
// shape an ALB's discovered membership (its alb.discovery block, the
// referenced discoverer entry, and the template backend's YAML-visible
// options) are identical between the outgoing and incoming configs, making
// it safe to seed the new manager with the outgoing membership
func discoveryConfigUnchanged(oldConf, newConf *config.Config, albName string,
	opts *ao.DiscoveryOptions,
) bool {
	if oldConf == nil {
		return false
	}
	oldBackend, ok := oldConf.Backends[albName]
	if !ok || oldBackend == nil || oldBackend.ALBOptions == nil ||
		oldBackend.ALBOptions.Discovery == nil {
		return false
	}
	if !reflect.DeepEqual(oldBackend.ALBOptions.Discovery, opts) {
		return false
	}
	if !reflect.DeepEqual(oldConf.Discovery[opts.DiscovererName],
		newConf.Discovery[opts.DiscovererName]) {
		return false
	}
	oldTmpl, newTmpl := oldConf.Backends[opts.TemplateBackend],
		newConf.Backends[opts.TemplateBackend]
	if oldTmpl == nil || newTmpl == nil {
		return false
	}
	return oldTmpl.ToYAML() == newTmpl.ToYAML()
}
