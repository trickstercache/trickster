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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

func TestDruidProviderIsRegistered(t *testing.T) {
	if SupportedProviders()[providers.Druid] == nil {
		t.Fatal("Druid provider is not registered")
	}
}

func TestPostgresProvidersShareOneNativeAdapter(t *testing.T) {
	supported := SupportedProviders()
	if supported[providers.Postgres] == nil || supported[providers.TimescaleDB] == nil {
		t.Fatal("postgres and its timescaledb alias must both be registered")
	}
	listeners := NativeListeners()
	adapter := listeners.GetByProvider(providers.Postgres)
	if adapter == nil || listeners.GetByProvider(providers.TimescaleDB) != adapter {
		t.Fatal("both provider names must resolve to the same native adapter")
	}
	if listeners.Get(adapter.Protocol()) != adapter {
		t.Fatal("the adapter must be registered under its own protocol")
	}
	for protocol, registered := range listeners {
		names := registered.Providers()
		if len(names) == 0 {
			t.Fatalf("%s serves no providers", protocol)
		}
		for _, name := range names {
			if !registered.ServesProvider(name) || listeners.GetForProvider(protocol, name) != registered {
				t.Fatalf("%s lists %q but does not serve it", protocol, name)
			}
		}
	}
	if listeners.GetByProvider(providers.Prometheus) != nil {
		t.Fatal("an HTTP-only provider must have no native adapter")
	}
	if supported[providers.GreptimeDB] == nil || listeners.GetForProvider("postgres", providers.GreptimeDB) != adapter ||
		!adapter.SupportsHTTP(providers.GreptimeDB) || adapter.SupportsHTTP(providers.Postgres) {
		t.Fatal("GreptimeDB must share pgwire without changing PostgreSQL's HTTP capability")
	}
	if listeners.GetByProvider(providers.GreptimeDB) != nil || len(listeners.ForProvider(providers.GreptimeDB)) != 2 {
		t.Fatal("GreptimeDB must retain both native adapters without an ambiguous default")
	}
}
