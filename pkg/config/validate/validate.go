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

package validate

import (
	stderrors "errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registry"
	ar "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

func Validate(c *config.Config) error {
	if c == nil {
		return errors.ErrInvalidOptions
	}
	if c.MgmtConfig != nil {
		if err := c.MgmtConfig.Validate(); err != nil {
			return err
		}
	}
	if c.Logging != nil {
		if _, err := c.Logging.Validate(); err != nil {
			return err
		}
	}
	if c.Metrics != nil {
		if _, err := c.Metrics.Validate(); err != nil {
			return err
		}
	}
	if err := Tracers(c); err != nil {
		return err
	}
	if err := Rewriters(c); err != nil {
		return err
	}
	if err := Rules(c); err != nil {
		return err
	}
	if err := Authenticators(c); err != nil {
		return err
	}
	if err := Caches(c); err != nil {
		return err
	}
	if err := NegativeCaches(c); err != nil {
		return err
	}
	if err := Backends(c); err != nil {
		return err
	}
	return Listeners(c)
}

func Rewriters(c *config.Config) error {
	if c == nil || len(c.RequestRewriters) == 0 {
		return nil
	}
	return c.RequestRewriters.Validate()
}

func Tracers(c *config.Config) error {
	if c == nil || len(c.TracingOptions) == 0 {
		return nil
	}
	return c.TracingOptions.Validate()
}

func Rules(c *config.Config) error {
	if c == nil || len(c.Rules) == 0 {
		return nil
	}
	return c.Rules.Validate()
}

func Caches(c *config.Config) error {
	if c == nil || len(c.Caches) == 0 {
		return nil
	}
	return c.Caches.Validate()
}

func NegativeCaches(c *config.Config) error {
	if c == nil || len(c.NegativeCacheConfigs) == 0 {
		return nil
	}
	nc, err := c.NegativeCacheConfigs.ValidateAndCompile()
	if err != nil {
		return err
	}
	c.CompiledNegativeCaches = nc
	return nil
}

func Authenticators(c *config.Config) error {
	if c == nil || len(c.Authenticators) == 0 {
		return nil
	}
	return c.Authenticators.Validate(ar.IsRegistered)
}

func Backends(c *config.Config) error {
	if c == nil {
		return errors.ErrNoValidBackends
	}
	if len(c.Backends) == 0 {
		return errors.ErrNoValidBackends
	}
	if err := c.Backends.ValidateConfigMappings(c.Caches, c.CompiledNegativeCaches,
		c.Rules, c.RequestRewriters, c.Authenticators, c.TracingOptions); err != nil {
		return err
	}
	serveTLS, err := c.Backends.ValidateTLSConfigs()
	if err != nil {
		return err
	}
	if serveTLS && c.Frontend != nil {
		c.Frontend.ServeTLS = true
	}
	return c.Backends.Validate()
}

// Listeners validates inbound listener definitions and backend mappings.
func Listeners(c *config.Config) error {
	if c == nil || len(c.Listeners) == 0 {
		return stderrors.New("no listeners configured")
	}

	mapped := make(map[string]int, len(c.Listeners))
	tlsMapped := make(map[string]bool, len(c.Listeners))
	for backendName, backend := range c.Backends {
		if backend == nil {
			continue
		}
		if backend.ListenerName == "" {
			backend.ListenerName = listener.DefaultFrontendName
		}
		if backend.ListenerName == mgmt.ListenerNameMgmt || backend.ListenerName == mgmt.ListenerNameMetrics {
			return fmt.Errorf("backend %q cannot use reserved listener %q", backendName, backend.ListenerName)
		}
		if _, ok := c.Listeners[backend.ListenerName]; !ok {
			return fmt.Errorf("backend %q references undefined listener %q", backendName, backend.ListenerName)
		}
		mapped[backend.ListenerName]++
		if backend.TLS != nil && backend.TLS.ServeTLS {
			tlsMapped[backend.ListenerName] = true
		}
	}

	ports := make(map[string]string)
	for name, options := range c.Listeners {
		if name == "" || options == nil {
			return stderrors.New("invalid empty listener configuration")
		}
		options.Protocol = strings.ToLower(options.Protocol)
		if options.Protocol == "" {
			options.Protocol = listener.ProtocolHTTP
		}
		if options.Protocol != listener.ProtocolHTTP && options.TLSListenPort > 0 {
			return fmt.Errorf("listener %q cannot configure a TLS port for protocol %q", name, options.Protocol)
		}
		if options.Protocol != listener.ProtocolHTTP && mapped[name] > 1 {
			return fmt.Errorf("listener %q with protocol %q can map to only one backend", name, options.Protocol)
		}
		if !listener.IsSupportedProtocol(options.Protocol) {
			return fmt.Errorf("listener %q uses unsupported protocol %q", name, options.Protocol)
		}
		if options.ListenPort < 0 || options.TLSListenPort < 0 {
			return fmt.Errorf("listener %q has an invalid listen port", name)
		}

		builtIn := name == listener.DefaultFrontendName ||
			name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics
		options.Active = name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics || mapped[name] > 0
		if !builtIn && mapped[name] == 0 {
			addWarning(c, fmt.Sprintf("listener %q is unused and will not be started", name))
		}

		if options.TLSListenPort > 0 && !tlsMapped[name] {
			addWarning(c, fmt.Sprintf(
				"listener %q TLS port is disabled because no mapped backend provides a TLS certificate", name))
			options.TLSListenPort = 0
			options.ServeTLS = false
		} else {
			options.ServeTLS = options.TLSListenPort > 0 && tlsMapped[name]
		}
		if options.Active && options.ListenPort == 0 && options.TLSListenPort == 0 {
			addWarning(c, fmt.Sprintf("listener %q has no enabled ports and will not be started", name))
		}

		if options.Active && options.ListenPort > 0 {
			if err := reserveListenerPort(ports, name, options.ListenAddress, options.ListenPort); err != nil {
				return err
			}
		}
		if options.Active && options.TLSListenPort > 0 {
			if err := reserveListenerPort(ports, name, options.TLSListenAddress, options.TLSListenPort); err != nil {
				return err
			}
		}
	}
	return nil
}

func addWarning(c *config.Config, warning string) {
	if slices.Contains(c.LoaderWarnings, warning) {
		return
	}
	c.LoaderWarnings = append(c.LoaderWarnings, warning)
}

func reserveListenerPort(ports map[string]string, listenerName, address string, port int) error {
	key := fmt.Sprintf("%s:%d", address, port)
	if existing, ok := ports[key]; ok {
		return fmt.Errorf("listeners %q and %q both use %s", existing, listenerName, key)
	}
	ports[key] = listenerName
	return nil
}

func RoutesRulesAndPools(c *config.Config, clients backends.Backends) error {
	caches := make(cache.Lookup)
	for k := range c.Caches {
		caches[k] = nil
	}
	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(0) // metrics router is exact-match only
	listenerRouters := make(map[string]router.Router)
	for name, options := range c.Listeners {
		if options != nil && options.Active &&
			name != mgmt.ListenerNameMgmt && name != mgmt.ListenerNameMetrics {
			listenerRouters[name] = lm.NewRouter()
		}
	}
	if len(listenerRouters) == 0 {
		listenerRouters[listener.DefaultFrontendName] = r
	}
	tracers, err := tr.RegisterAll(c, true)
	if err != nil {
		return err
	}
	err = routing.RegisterProxyRoutesForListeners(c, clients, listenerRouters, mr, caches, tracers, true)
	if err != nil {
		return err
	}
	// these validations can't be performed until the router tree is constructed
	err = rule.ValidateOptions(clients, c.CompiledRewriters)
	if err != nil {
		return err
	}
	return alb.ValidateClients(clients)
}
