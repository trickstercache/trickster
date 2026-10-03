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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
)

// ErrACMEWithKubernetes is returned when ACME is configured alongside the Kubernetes controller
var ErrACMEWithKubernetes = errors.New(
	"acme is not supported with the kubernetes controller; use cert-manager to issue Secrets")

// ACME validates the acme section and each backend's tls.acme against what they reference;
// it runs after Listeners, which settles each listener's TLS state
func ACME(c *config.Config) error {
	if c == nil {
		return nil
	}
	if c.ACME != nil {
		if err := c.ACME.Validate(); err != nil {
			return fmt.Errorf("acme: %w", err)
		}
		if c.ACME.IsEnabled() && c.Kubernetes.IsEnabled() {
			return ErrACMEWithKubernetes
		}
		if err := acmeStorageCache(c); err != nil {
			return err
		}
		if err := acmeOnDemandListeners(c); err != nil {
			return err
		}
	}
	owners := make(map[string]string)
	for _, name := range slices.Sorted(maps.Keys(c.Backends)) {
		b := c.Backends[name]
		if b == nil || b.IsTemplate || b.TLS == nil || b.TLS.ACME == nil {
			continue
		}
		if err := acmeBackend(c, name, b.Hosts, b.ListenerNames, b.TLS.ACME, owners); err != nil {
			return err
		}
	}
	return nil
}

func acmeBackend(c *config.Config, name string, hosts, listenerNames []string,
	o *acmeopts.BackendOptions, owners map[string]string,
) error {
	if !c.ACME.IsEnabled() {
		return fmt.Errorf("backend %q sets tls.acme but the acme section declares no issuers", name)
	}
	iss, ok := c.ACME.Issuers[o.Issuer]
	if !ok {
		return fmt.Errorf("backend %q references undefined acme issuer %q", name, o.Issuer)
	}
	domains, err := o.ResolveDomains(hosts)
	if err != nil {
		return fmt.Errorf("backend %q: %w", name, err)
	}
	dns01 := iss.HasChallenge(acmeopts.ChallengeDNS01)
	for _, d := range domains {
		if hostnames.IsWildcard(d) && !dns01 {
			return fmt.Errorf("backend %q: wildcard domain %q requires an issuer using dns-01", name, d)
		}
		if owner, ok := owners[d]; ok && owner != o.Issuer {
			return fmt.Errorf("backend %q: acme domain %q is already issued by %q", name, d, owner)
		}
		owners[d] = o.Issuer
	}
	o.ResolvedDomains = domains
	http01 := iss.HasChallenge(acmeopts.ChallengeHTTP01)
	for _, ln := range listenerNames {
		lo := c.Listeners[ln]
		if lo == nil {
			continue
		}
		if lo.Protocol != listener.ProtocolHTTP || !lo.ServeTLS || lo.TLSListenPort <= 0 {
			return fmt.Errorf("backend %q uses tls.acme, which requires listener %q to be an "+
				"http listener with a tls_port", name, ln)
		}
		if http01 && lo.ListenPort <= 0 {
			addWarning(c, fmt.Sprintf("listener %q has no plaintext port, so http-01 challenges "+
				"for backend %q cannot be answered there", ln, name))
		}
	}
	return nil
}

func acmeStorageCache(c *config.Config) error {
	name := c.ACME.RedisCacheName()
	if name == "" {
		return nil
	}
	co, ok := c.Caches[name]
	if !ok || co == nil {
		return fmt.Errorf("acme.storage.redis references undefined cache %q", name)
	}
	if strings.ToLower(co.Provider) != cp.Redis || co.Redis == nil {
		return fmt.Errorf("acme.storage.redis cache %q is not a redis cache", name)
	}
	return nil
}

func acmeOnDemandListeners(c *config.Config) error {
	od := c.ACME.OnDemand
	if od == nil {
		return nil
	}
	for _, ln := range od.Listeners {
		lo := c.Listeners[ln]
		if lo == nil {
			return fmt.Errorf("acme.on_demand references undefined listener %q", ln)
		}
		if lo.Protocol != listener.ProtocolHTTP || lo.TLSListenPort <= 0 {
			return fmt.Errorf("acme.on_demand listener %q must be an http listener with a tls_port", ln)
		}
	}
	return nil
}

func acmeOnDemandListener(c *config.Config, name string) bool {
	return c.ACME.IsEnabled() && c.ACME.OnDemand != nil && slices.Contains(c.ACME.OnDemand.Listeners, name)
}
