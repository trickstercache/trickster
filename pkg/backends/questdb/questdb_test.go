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

package questdb

import (
	"net/http"
	"slices"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestClientContract(t *testing.T) {
	o := bo.New()
	o.Provider, o.OriginURL = providers.QuestDB, "http://db.example:9000/prefix"
	if err := o.Initialize("questdb"); err != nil {
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
	if len(paths) != 1 || paths[0].Path != "/" || paths[0].HandlerName != providers.Proxy ||
		paths[0].MatchType != matching.PathMatchTypePrefix || !slices.Equal(paths[0].Methods, methods.AllHTTPMethods()) {
		t.Fatalf("unexpected paths: %+v", paths)
	}
	if handlers := backend.Handlers(); handlers["proxy"] == nil || handlers["health"] == nil {
		t.Fatal("missing pass-through handlers")
	}
}

func TestProxyHandler(t *testing.T) {
	backend, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backend.DefaultPathConfigs,
		http.StatusOK, "{}", nil, providers.QuestDB, "/execute", "debug")
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	rsc := request.GetResources(r)
	backend, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := backend.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = client.HTTPClient()
	client.ProxyHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected proxy status %d", w.Code)
	}
}

func TestStepAlignments(t *testing.T) {
	client, err := NewClient("questdb", bo.New(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	supported, def := client.(*Client).StepAlignments()
	if supported != sqlanalyzer.StepAlignments || def != sqlanalyzer.DefaultStepAlignment {
		t.Fatalf("unexpected step alignment profile: (%s; %s)", supported, def)
	}
}

func TestEngineContract(t *testing.T) {
	e := Engine()
	if e.Name() != providers.QuestDB || e.Dialect() != providers.QuestDB || e.DefaultPort() != DefaultPort ||
		e.Analyzer() == nil || e.Defaults().UpstreamTLSMode != pgo.TLSModeDisable ||
		!e.TimeSemantics().NaiveTimestampsAreUTC || e.TimeSemantics().AssumedDateStyle != "ISO" {
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
	defaults, ok := e.(pgwire.SessionDefaultsEngine)
	if !ok || defaults.SessionDefaultsProbe().SQL != "SELECT current_setting('extra_float_digits'), current_setting('bytea_output')" ||
		!slices.Equal(defaults.SessionDefaultsProbe().Names, []string{"extra_float_digits", "bytea_output"}) {
		t.Fatalf("unexpected defaults probe: %+v", defaults)
	}
	settings, ok := e.(pgwire.SessionSettingsEngine)
	if !ok {
		t.Fatal("QuestDB must describe its session settings before caching")
	}
	profile := settings.SessionSettings()
	if profile.Aliases["time_zone"] != "timezone" || !profile.UnconfirmedStartup {
		t.Fatalf("unexpected setting aliases/startup policy: %+v", profile)
	}
	for _, name := range []string{"timezone", "datestyle", "intervalstyle", "extra_float_digits", "bytea_output", "search_path"} {
		if _, ok := profile.Tracked[name]; !ok {
			t.Fatalf("untracked setting: %s", name)
		}
	}
}

func TestHealthProtocolSelection(t *testing.T) {
	o := bo.New()
	o.Provider, o.OriginURL = providers.QuestDB, "http://db.example:9000/prefix/"
	o.HasHTTPListener = true
	if err := o.Initialize("questdb"); err != nil {
		t.Fatal(err)
	}
	backend, err := NewClient(o.Name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := backend.(*Client)
	if c.HealthCheckProbe() != nil {
		t.Fatal("HTTP deployments must use the HTTP health check")
	}
	health := c.DefaultHealthCheckConfig()
	if health.Scheme != "http" || health.Host != "db.example:9000" || health.Path != "/prefix/execute" ||
		health.Query != "query=SELECT%201" {
		t.Fatalf("unexpected HTTP health check: %+v", health)
	}

	o.HasHTTPListener = false
	o.Postgres = pgo.New()
	o.Postgres.UpstreamURL = "postgres://origin:secret@127.0.0.1:8812/qdb"
	if c.HealthCheckProbe() == nil {
		t.Fatal("native-only deployments must use the pgwire health probe")
	}
}
