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
package pgwire

import (
	"errors"
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
)

var errNoMappedBackend = errors.New("no mapped backend")

type nativeListenerAdapter struct {
	engines Engines
}

var _ native.Adapter = (*nativeListenerAdapter)(nil)

// NewNativeListenerAdapter returns the postgres protocol's one adapter, which
// serves every provider that has an engine in the registry.
func NewNativeListenerAdapter(engines Engines) native.Adapter {
	// a pointer, because the struct holds a map and adapters are compared as interfaces
	return &nativeListenerAdapter{engines: engines}
}

func (nativeListenerAdapter) SupportsHTTP() bool { return false }

func (nativeListenerAdapter) Protocol() string { return listenerconfig.ProtocolPostgres }

func (a nativeListenerAdapter) ServesProvider(provider string) bool {
	return a.engines.Get(provider) != nil
}

func (a nativeListenerAdapter) Providers() []string { return a.engines.Names() }

func (nativeListenerAdapter) Configured(o *listenerconfig.Options) bool {
	return o != nil && o.Postgres != nil
}

func (nativeListenerAdapter) ValidateListener(o *listenerconfig.Options) error {
	if o == nil {
		return errors.New("nil postgres listener options")
	}
	if o.Postgres == nil {
		o.Postgres = pgo.NewListener()
	}
	return o.Postgres.Validate()
}

func (a nativeListenerAdapter) ValidateBackend(o *bo.Options) error {
	if o != nil && o.Postgres != nil {
		if err := o.Postgres.Validate(); err != nil {
			return err
		}
	}
	_, err := a.configFromOptions(o)
	return err
}

// ValidateUserRouter rejects User Router ALBs until routing by startup user is implemented.
func (nativeListenerAdapter) ValidateUserRouter(_ *config.Config, name string, _ *bo.Options) error {
	return fmt.Errorf("postgres user router %q: user routing is not supported for the postgres protocol", name)
}

func (a nativeListenerAdapter) Describe(c *config.Config, listenerName string) (native.Descriptor, error) {
	protocolConfig, err := a.listenerConfig(c, listenerName)
	if err != nil {
		return native.Descriptor{}, err
	}
	return native.Descriptor{RestartKey: protocolConfig.RestartKey}, nil
}

func (a nativeListenerAdapter) Build(request native.BuildRequest) (listener.ProtocolServer, error) {
	protocolConfig, err := a.listenerConfig(request.Config, request.ListenerName)
	if err != nil {
		return nil, err
	}
	if client := request.BackendClients.Get(protocolConfig.BackendName); client != nil {
		protocolConfig.CacheProvider = client
	}
	protocolConfig.InboundTLS, err = request.Config.TLSCertConfigForListener(request.ListenerName)
	if err != nil {
		return nil, err
	}
	return NewServer(*protocolConfig)
}

func (nativeListenerAdapter) RouteResolver(native.BuildRequest) backends.RouteResolver { return nil }

func (a nativeListenerAdapter) configFromOptions(o *bo.Options) (Config, error) {
	if o == nil {
		return Config{}, errors.New("nil postgres backend options")
	}
	return ConfigFromOptions(o, a.engines.Get(strings.ToLower(o.Provider)))
}

func (a nativeListenerAdapter) listenerConfig(c *config.Config, listenerName string) (*Config, error) {
	// returns the configuration of the single backend mapped to a
	// postgres listener; common listener validation guarantees uniqueness.
	if c == nil {
		return nil, errNoMappedBackend
	}
	for backendName, o := range c.Backends {
		if !o.UsesListener(listenerName) {
			continue
		}
		protocolConfig, err := a.configFromOptions(o)
		if err != nil {
			return nil, err
		}
		protocolConfig.BackendName = backendName
		if listenerOptions := c.Listeners[listenerName]; listenerOptions != nil {
			protocolConfig.ApplyListenerOptions(listenerOptions.Postgres)
		}
		protocolConfig.RestartKey = backendName + ":" + protocolConfig.RestartKey
		return &protocolConfig, nil
	}
	return nil, errNoMappedBackend
}
