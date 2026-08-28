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

// Package registry maps autodiscovery provider names to their Discoverer
// constructors, mirroring pkg/backends/providers/registry
package registry

import (
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/discovery/dns"
	"github.com/trickstercache/trickster/v2/pkg/discovery/file"
	"github.com/trickstercache/trickster/v2/pkg/discovery/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
)

// SupportedProviders returns the registry of Discoverer constructors by
// provider name. Future providers (see the roadmap in
// docs/developer/discovery-providers.md) register here.
func SupportedProviders() discovery.Lookup {
	return discovery.Lookup{
		providers.Kubernetes: kubernetes.New,
		providers.DNSSRV:     dns.NewSRV,
		providers.DNSA:       dns.NewA,
		providers.File:       file.New,
	}
}

// New constructs a Discoverer for the given named discoverer Options,
// dispatching on its Provider
func New(o *options.Options) (discovery.Discoverer, error) {
	if o == nil {
		return nil, errors.New("nil discoverer options")
	}
	if f, ok := SupportedProviders()[o.Provider]; ok && f != nil {
		return f(o.Name, o)
	}
	return nil, fmt.Errorf("no discoverer implementation registered for provider %q (discoverer %q)",
		o.Provider, o.Name)
}
