/*
 * Copyright 2026 The Trickster Authors
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
	"testing"

	clickhouse "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/authenticator"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/providers/basic"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

func TestRegistryLookup(t *testing.T) {
	data := map[string]any{"options": &options.Options{Name: "test"}}
	for _, provider := range []types.Provider{basic.ID, clickhouse.ID} {
		if !IsRegistered(provider) {
			t.Errorf("provider %q is not registered", provider)
		}
		a, err := New(provider, data)
		if err != nil || a == nil {
			t.Errorf("New(%q) = %T, %v", provider, a, err)
		}
	}
	if IsRegistered("unknown") {
		t.Error("unknown provider is registered")
	}
	if a, err := New("unknown", data); a != nil || !errors.Is(err, ErrUnsupportedAuthenticator) {
		t.Errorf("New(unknown) = %T, %v", a, err)
	}
}

func TestNewObserverFromProviderName(t *testing.T) {
	data := map[string]any{"options": &options.Options{Name: "test", ObserveOnly: true}}
	for _, provider := range []string{
		providers.Prometheus,
		providers.ReverseProxy,
		providers.Proxy,
		providers.ReverseProxyCache,
		providers.ReverseProxyCacheShort,
		providers.ReverseProxyShort,
		providers.ClickHouse,
	} {
		a, err := NewObserverFromProviderName(provider, data)
		if err != nil || a == nil {
			t.Errorf("NewObserverFromProviderName(%q) = %T, %v", provider, a, err)
		}
	}
	if a, err := NewObserverFromProviderName(providers.MySQL, data); a != nil ||
		!errors.Is(err, ErrUnsupportedAuthenticator) {
		t.Errorf("unsupported provider = %T, %v", a, err)
	}
	if a, err := NewObserverFromProviderName(providers.Prometheus, nil); a != nil || err == nil {
		t.Errorf("invalid options = %T, %v", a, err)
	}
}
