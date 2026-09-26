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

package greptimedb

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestClientContract(t *testing.T) {
	o := bo.New()
	o.Provider, o.OriginURL = providers.GreptimeDB, "http://db.example:4000/prefix"
	if err := o.Initialize("greptime"); err != nil {
		t.Fatal(err)
	}
	backend, err := NewClient(o.Name, o, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if backend.Configuration() != o || backend.Name() != o.Name {
		t.Fatal("lost backend identity")
	}
	paths := backend.DefaultPathConfigs(o)
	if len(paths) != 7 || paths[0].Path != "/" || paths[0].HandlerName != "proxy" ||
		paths[0].MatchType != matching.PathMatchTypePrefix || !slices.Equal(paths[0].Methods, methods.AllHTTPMethods()) {
		t.Fatalf("unexpected paths: %+v", paths)
	}
	paths[0].Path = "/changed"
	if backend.DefaultPathConfigs(o)[0].Path != "/" {
		t.Fatal("default paths share mutable state")
	}
	if handlers := backend.Handlers(); len(handlers) != 10 || handlers["proxy"] == nil || handlers["health"] == nil || handlers["query"] == nil || handlers["sql"] == nil {
		t.Fatal("missing HTTP handlers")
	}
}

func TestEngineContract(t *testing.T) {
	e := Engine()
	if e.Name() != providers.GreptimeDB || e.Dialect() != providers.GreptimeDB || e.DefaultPort() != DefaultPort ||
		e.Analyzer() == nil || e.Defaults().UpstreamTLSMode != pgo.TLSModeDisable || !e.TimeSemantics().LosslessFloatText {
		t.Fatal("unexpected engine contract")
	}
	if !e.(pgwire.HTTPEngine).SupportsHTTP() {
		t.Fatal("engine must expose HTTP")
	}
	for _, oid := range []uint32{pgwire.OIDTimestamp, pgwire.OIDTimestampTZ, pgwire.OIDDate, pgwire.OIDInt8, 0} {
		want, wantOK := pgwire.StandardTimeAxis(oid)
		got, gotOK := e.TimeAxis(oid)
		if got != want || gotOK != wantOK {
			t.Fatalf("OID %d: incompatible time axis", oid)
		}
	}
}

func TestSessionContract(t *testing.T) {
	e := Engine()
	probe := e.(pgwire.SessionDefaultsEngine).SessionDefaultsProbe()
	if probe.SQL != "SHOW TIMEZONE; SHOW DateStyle; SHOW IntervalStyle" ||
		!slices.Equal(probe.Names, []string{"timezone", "datestyle", "intervalstyle"}) {
		t.Fatalf("unexpected defaults probe: %+v", probe)
	}
	settings := e.(pgwire.SessionSettingsEngine).SessionSettings()
	if !settings.LocalPersists || settings.Aliases["time_zone"] != "timezone" {
		t.Fatal("lost GreptimeDB's SET LOCAL or time_zone semantics")
	}
	for _, name := range []string{"timezone", "datestyle", "intervalstyle", "bytea_output", "search_path"} {
		if _, ok := settings.Tracked[name]; !ok {
			t.Fatalf("untracked setting: %s", name)
		}
	}
	for _, name := range []string{"application_name", "extra_float_digits", "standard_conforming_strings"} {
		if _, ok := settings.Neutral[name]; !ok {
			t.Fatalf("no-op setting changes cache state: %s", name)
		}
	}
	if e.TimeSemantics().AssumedTimeZone != "" {
		t.Fatal("the server timezone is configurable, not an engine guarantee")
	}
}

func TestProxyHandler(t *testing.T) {
	client := &Client{}
	origin, w, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs,
		http.StatusOK, "{}", nil, providers.GreptimeDB, "/v1/sql", "error")
	if err != nil {
		t.Fatal(err)
	}
	defer origin.Close()
	rsc := request.GetResources(r)
	backend, err := NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client = backend.(*Client)
	rsc.BackendClient, rsc.BackendOptions.HTTPClient = client, client.HTTPClient()
	client.ProxyHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected proxy status %d", w.Code)
	}
}

func TestHealthProtocolSelection(t *testing.T) {
	for _, httpListener := range []bool{true, false} {
		o := bo.New()
		o.Provider, o.OriginURL = providers.GreptimeDB, "http://db.example:4000/prefix"
		o.HasHTTPListener = httpListener
		o.Postgres = pgo.New()
		o.Postgres.UpstreamURL = "postgres://origin:secret@127.0.0.1:9/public"
		if err := o.Initialize("greptime"); err != nil {
			t.Fatal(err)
		}
		backend, err := NewClient(o.Name, o, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := backend.(*Client)
		health := c.DefaultHealthCheckConfig()
		probe := c.HealthCheckProbe()
		if httpListener {
			if probe != nil || health.Scheme != "http" || health.Host != "db.example:4000" || health.Path != "/prefix/health" {
				t.Fatal("mixed backend must use HTTP health")
			}
		} else {
			if probe == nil || health.Host != "" || health.Scheme != "" || health.Path != "" {
				t.Fatal("native-only backend must use pgwire health even with an HTTP origin_url")
			}
			o.Postgres.UpstreamURL = "://bad"
			if err := c.HealthCheckProbe()(context.Background()); !errors.Is(err, errProbeConfig) {
				t.Fatalf("expected sanitized invalid probe: %v", err)
			}
		}
	}
	backend, err := NewClient("nil-options", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := backend.(*Client)
	if c.DefaultHealthCheckConfig() == nil || !errors.Is(c.HealthCheckProbe()(context.Background()), errProbeConfig) {
		t.Fatal("nil options must fail the probe without panicking")
	}
}

func TestMySQLHealthProtocolSelection(t *testing.T) {
	o := bo.New()
	o.Provider, o.OriginURL = providers.GreptimeDB, "http://db.example:4000"
	o.NativeListenerProtocols = []string{listener.ProtocolMySQL}
	o.MySQL = mo.New()
	o.MySQL.UpstreamURL = "mysql://reader:dev-password@127.0.0.1:9/public"
	o.Postgres = pgo.New()
	o.Postgres.UpstreamURL = "://unused-postgres"
	if err := o.Initialize("greptime"); err != nil {
		t.Fatal(err)
	}
	backend, err := NewClient(o.Name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := backend.(*Client)
	probe := c.HealthCheckProbe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if probe == nil || errors.Is(probe(ctx), errProbeConfig) {
		t.Fatal("MySQL-only backend selected PostgreSQL health options")
	}
	o.MySQL.UpstreamURL = "://invalid-mysql"
	if err := c.HealthCheckProbe()(ctx); !errors.Is(err, errProbeConfig) {
		t.Fatalf("invalid MySQL health config was not sanitized: %v", err)
	}
}
