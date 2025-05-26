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

package registry

import (
	"errors"

	clickhouse "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/authenticator"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/providers/basic"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

var ErrUnsupportedAuthenticator = errors.New("unsupported authenticator")

// this slice is the one and only place to aggregate all registered Authenticators
var registry = []types.RegistryEntry{
	basic.RegistryEntry(),      //  BasicAuth
	clickhouse.RegistryEntry(), // ClickHouse Auth (Basic + url params)
}

var registryByName = compileSupportedByName()

func compileSupportedByName() map[types.Provider]types.NewAuthenticatorFunc {
	out := make(map[types.Provider]types.NewAuthenticatorFunc, len(registry)*2)
	for _, entry := range registry {
		out[entry.Provider] = entry.New
	}
	return out
}

func New(name types.Provider, data map[string]any) (types.Authenticator, error) {
	if f, ok := registryByName[name]; ok && f != nil {
		return f(data)
	}
	return nil, ErrUnsupportedAuthenticator
}

func IsRegistered(name types.Provider) bool {
	_, ok := registryByName[name]
	return ok
}

func NewObserverFromProviderName(backendProvider string, data map[string]any) (types.Authenticator, error) {
	var a types.Authenticator
	var err error
	switch backendProvider {
	case providers.Prometheus, providers.ReverseProxy, providers.Proxy,
		providers.ReverseProxyCache, providers.ReverseProxyCacheShort,
		providers.ReverseProxyShort:
		a, err = basic.New(data)
	case providers.ClickHouse:
		a, err = clickhouse.New(data)
	default:
		return nil, ErrUnsupportedAuthenticator
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}
