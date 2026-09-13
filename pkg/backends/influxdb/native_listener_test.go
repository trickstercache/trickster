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

package influxdb

import (
	"context"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	io "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flightsql"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
)

func flightTestConfig() *config.Config {
	c := config.NewConfig()
	c.Listeners["flight"] = listenerconfig.New("flight")
	c.Listeners["flight"].Protocol = listenerconfig.ProtocolFlightSQL
	backend := bo.New()
	backend.Name = "influx"
	backend.Provider = providers.InfluxDB
	backend.OriginURL = "http://127.0.0.1:8181"
	backend.ListenerNames = []string{"flight"}
	c.Backends = bo.Lookup{"influx": backend}
	return c
}

func flightTestBuildRequest(c *config.Config,
	clients backends.Backends,
) native.BuildRequest {
	return native.BuildRequest{
		Config: c, ListenerName: "flight", Listener: c.Listeners["flight"],
		BackendClients: clients,
	}
}

func TestFlightNativeListenerAdapterContract(t *testing.T) {
	adapter := NativeListenerAdapter()
	if adapter.Protocol() != listenerconfig.ProtocolFlightSQL {
		t.Fatalf("Protocol() = %q", adapter.Protocol())
	}
	if !adapter.SupportsHTTP() {
		t.Fatal("SupportsHTTP() = false")
	}
	if adapter.Configured(listenerconfig.New("flight")) {
		t.Fatal("Configured() reported listener-level options")
	}
	if err := adapter.ValidateListener(nil); err == nil {
		t.Fatal("ValidateListener(nil) succeeded")
	}
	if err := adapter.ValidateListener(listenerconfig.New("flight")); err != nil {
		t.Fatalf("ValidateListener() = %v", err)
	}
	if err := adapter.ValidateBackend(nil); err == nil {
		t.Fatal("ValidateBackend(nil) succeeded")
	}
	valid := flightTestConfig().Backends["influx"]
	if err := adapter.ValidateBackend(valid); err != nil {
		t.Fatalf("ValidateBackend(valid) = %v", err)
	}
	if err := adapter.ValidateUserRouter(nil, "flight", nil); err == nil {
		t.Fatal("ValidateUserRouter() succeeded; Flight SQL has no user routing")
	}
	if resolver := adapter.RouteResolver(native.BuildRequest{}); resolver != nil {
		t.Fatal("RouteResolver() returned a resolver")
	}
}

func TestFlightUpstreamAddress(t *testing.T) {
	o := flightTestConfig().Backends["influx"]
	if addr, err := flightUpstreamAddress(o); err != nil || addr != "127.0.0.1:8181" {
		t.Fatalf("flightUpstreamAddress(origin) = %q, %v", addr, err)
	}
	o.InfluxDB = &io.Options{FlightUpstreamAddress: "influx-flight:8182"}
	if addr, err := flightUpstreamAddress(o); err != nil || addr != "influx-flight:8182" {
		t.Fatalf("flightUpstreamAddress(override) = %q, %v", addr, err)
	}
	o.InfluxDB = nil
	o.OriginURL = "://bad"
	if _, err := flightUpstreamAddress(o); err == nil {
		t.Fatal("flightUpstreamAddress(invalid origin) succeeded")
	}
	o.OriginURL = "/pathonly"
	if _, err := flightUpstreamAddress(o); err == nil {
		t.Fatal("flightUpstreamAddress(hostless origin) succeeded")
	}
}

func TestFlightNativeBackendSelection(t *testing.T) {
	if _, _, err := nativeBackend(nil, "flight"); err == nil {
		t.Fatal("nativeBackend(nil config) succeeded")
	}
	c := flightTestConfig()
	name, o, err := nativeBackend(c, "flight")
	if err != nil || name != "influx" || o == nil {
		t.Fatalf("nativeBackend() = %q, %v, %v", name, o, err)
	}
	if _, _, err := nativeBackend(c, "unmapped"); err == nil {
		t.Fatal("nativeBackend(unmapped listener) succeeded")
	}

	second := bo.New()
	second.Name = "influx2"
	second.Provider = providers.InfluxDB
	second.OriginURL = "http://127.0.0.1:8282"
	second.ListenerNames = []string{"flight"}
	c.Backends["influx2"] = second
	if _, _, err := nativeBackend(c, "flight"); err == nil {
		t.Fatal("nativeBackend(two mapped backends) succeeded")
	}
	delete(c.Backends, "influx2")

	c.Backends["influx"].Provider = providers.Prometheus
	if _, _, err := nativeBackend(c, "flight"); err == nil {
		t.Fatal("nativeBackend(non-InfluxDB backend) succeeded")
	}
}

func TestFlightNativeListenerDescribe(t *testing.T) {
	adapter := nativeListenerAdapter{}
	if _, err := adapter.Describe(nil, "flight"); err == nil {
		t.Fatal("Describe(nil) succeeded")
	}
	c := flightTestConfig()
	first, err := adapter.Describe(c, "flight")
	if err != nil || first.RestartKey == "" {
		t.Fatalf("Describe() = %+v, %v", first, err)
	}

	// equivalent configs produce a stable key
	again, err := adapter.Describe(flightTestConfig(), "flight")
	if err != nil || again.RestartKey != first.RestartKey {
		t.Fatalf("equivalent configs produced different restart keys: %v", err)
	}

	// listener bindings don't alter identity
	bound := flightTestConfig()
	bound.Backends["influx"].ListenerNames = []string{"flight", "flight2"}
	bound.Listeners["flight2"] = bound.Listeners["flight"].Clone()
	rebound, err := adapter.Describe(bound, "flight")
	if err != nil || rebound.RestartKey != first.RestartKey {
		t.Fatalf("listener binding changed restart key: %v", err)
	}

	// backend option changes do alter identity
	changed := flightTestConfig()
	changed.Backends["influx"].InfluxDB = &io.Options{FlightUpstreamTLS: true}
	other, err := adapter.Describe(changed, "flight")
	if err != nil || other.RestartKey == first.RestartKey {
		t.Fatalf("backend option change did not alter restart key: %v", err)
	}
}

func TestFlightNativeListenerBuild(t *testing.T) {
	adapter := nativeListenerAdapter{}
	if _, err := adapter.Build(native.BuildRequest{}); err == nil {
		t.Fatal("Build(empty) succeeded")
	}
	c := flightTestConfig()
	if _, err := adapter.Build(flightTestBuildRequest(c, nil)); err == nil {
		t.Fatal("Build(no backend clients) succeeded")
	}

	client, err := NewClient("influx", c.Backends["influx"], nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Backends["influx"].InfluxDB = &io.Options{
		FlightCacheTTL: 300000000000, // 5m
	}
	server, err := adapter.Build(flightTestBuildRequest(c,
		backends.Backends{"influx": client}))
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	fs, ok := server.(*flightsql.ProtocolServer)
	if !ok || fs.ProtocolRestartKey() == "" {
		t.Fatalf("built server = %T with restart key %q", server, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
