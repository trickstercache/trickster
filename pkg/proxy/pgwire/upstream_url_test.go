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

package pgwire

import (
	"strings"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"

	"go.yaml.in/yaml/v3"
)

type mixedTestEngine struct{ testEngine }

func (mixedTestEngine) Name() string        { return "mixed" }
func (mixedTestEngine) DefaultPort() string { return "4003" }
func (mixedTestEngine) SupportsHTTP() bool  { return true }

func TestMixedUpstreamResolution(t *testing.T) {
	for _, tt := range []struct {
		name, origin, override string
		want                   Upstream
	}{
		{
			"HTTP host only", "http://http-user:http-secret@db.example:4000/v1/sql?db=other", "",
			Upstream{Address: "db.example:4003", Host: "db.example"},
		},
		{
			"HTTPS IPv6", "https://[::1]:4000/v1/prometheus", "",
			Upstream{Address: "[::1]:4003", Host: "::1"},
		},
		{
			"explicit PG override", "http://db.example:4000", "postgresql://sql:sql%20secret@pg.example:6432/my%20db",
			Upstream{Address: "pg.example:6432", Host: "pg.example", User: "sql", Password: "sql secret", Database: "my db"},
		},
		{
			"PG origin", "postgres://sql:secret@db.example/public", "",
			Upstream{Address: "db.example:4003", Host: "db.example", User: "sql", Password: "secret", Database: "public"},
		},
		{
			"override wins", "postgres://old:old@old.example/old", "postgres://new:new@new.example/new",
			Upstream{Address: "new.example:4003", Host: "new.example", User: "new", Password: "new", Database: "new"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := bo.New()
			data, err := yaml.Marshal(map[string]any{"origin_url": tt.origin, "postgres": map[string]string{"upstream_url": tt.override}})
			if err != nil {
				t.Fatal(err)
			}
			if err = yaml.Unmarshal(data, o); err != nil {
				t.Fatal(err)
			}
			c, err := ConfigFromOptions(o, mixedTestEngine{})
			if err != nil {
				t.Fatal(err)
			}
			if c.Upstream != tt.want {
				t.Fatalf("got %+v, want %+v", c.Upstream, tt.want)
			}
		})
	}
}

func TestMixedUpstreamRejections(t *testing.T) {
	for _, tt := range []struct{ name, origin, override string }{
		{"unsupported origin", "mysql://db.example/public", ""},
		{"missing origin host", "http:///v1/sql", ""},
		{"invalid override", "http://db.example:4000", "://bad"},
		{"HTTP override", "http://db.example:4000", "http://db.example:4003"},
		{"missing override host", "http://db.example:4000", "postgres:///public"},
		{"invalid override port", "http://db.example:4000", "postgres://db.example:65536/public"},
		{"malformed credentials", "http://db.example:4000", "postgres://user:secret%zz@db.example/public"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := bo.New()
			o.OriginURL = tt.origin
			o.Postgres = pgo.New()
			o.Postgres.UpstreamURL = tt.override
			_, err := ConfigFromOptions(o, mixedTestEngine{})
			if err == nil {
				t.Fatal("expected invalid upstream to fail")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed upstream credentials")
			}
		})
	}
	o := configTestOptions()
	o.OriginURL = "http://http-user:http-secret@db.example:4000/path"
	if _, err := ConfigFromOptions(o, mixedTestEngine{}); err == nil {
		t.Fatal("HTTP credentials must not enable terminated pgwire authentication")
	}
	if _, err := ConfigFromOptions(o, testEngine{}); err == nil {
		t.Fatal("a native-only engine must still reject HTTP origins")
	}
}

func TestMixedUpstreamReloadAndTLS(t *testing.T) {
	o := configTestOptions()
	o.OriginURL = "https://http.example:4000/base"
	o.Postgres = pgo.New()
	o.Postgres.UpstreamURL = "postgres://origin:password@sql.example/public"
	o.Postgres.UpstreamTLSMode = pgo.TLSModeVerifyFull
	first, err := ConfigFromOptions(o, mixedTestEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Upstream.TLS == nil || first.Upstream.TLS.ServerName != "sql.example" {
		t.Fatal("TLS must verify the resolved pgwire host, not the HTTP host")
	}
	for _, raw := range []string{
		"postgres://origin:rotated@sql.example/public",
		"postgres://origin:password@new.example/public",
		"postgres://origin:password@sql.example/other",
	} {
		clone := o.Clone()
		clone.Postgres.UpstreamURL = raw
		next, err := ConfigFromOptions(clone, mixedTestEngine{})
		if err != nil {
			t.Fatal(err)
		}
		if first.RestartKey == next.RestartKey {
			t.Fatal("upstream change did not restart the listener")
		}
	}
	if o.Postgres.UpstreamURL != "postgres://origin:password@sql.example/public" {
		t.Fatal("cloning options changed the original pgwire URL")
	}
}

func TestNativeHTTPIsProviderSpecific(t *testing.T) {
	a := NewNativeListenerAdapter(NewEngines(testEngine{}, mixedTestEngine{}))
	for provider, want := range map[string]bool{"postgres": false, "timescaledb": false, "mixed": true, "unknown": false} {
		if got := a.SupportsHTTP(provider); got != want {
			t.Errorf("%s: got %t, want %t", provider, got, want)
		}
	}
}
