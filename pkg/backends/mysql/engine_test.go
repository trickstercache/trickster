/*
 * Copyright 2026 The Trickster Authors
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package mysql

import (
	"strings"
	"testing"
	"time"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"

	vtmysql "vitess.io/vitess/go/mysql"
)

type testEngine struct{}

var (
	nativeProtocolConfig = (nativeListenerAdapter{}).nativeProtocolConfig
	nativeRouteRuntime   = (nativeListenerAdapter{}).nativeRouteRuntime
	isNativeRouter       = (nativeListenerAdapter{}).isNativeRouter
	isNativeUserRouter   = (nativeListenerAdapter{}).isNativeUserRouter
	isNativeBalancer     = (nativeListenerAdapter{}).isNativeBalancer
)

func (testEngine) Name() string                                      { return "test-engine" }
func (testEngine) DefaultPort() string                               { return "4002" }
func (testEngine) SupportsHTTP() bool                                { return true }
func (testEngine) Analyzer(SessionView) sqlanalyzer.DialectAnalyzer  { return nil }
func (testEngine) StreamState(*vtmysql.Conn) (uint16, uint16, error) { return 2, 7, nil }

func TestEngineProtocolConfig(t *testing.T) {
	o := validBackendOptions()
	o.Provider, o.OriginURL = "test-engine", "http://metrics.example:4000"
	o.MySQL = mo.New()
	o.MySQL.UpstreamURL = "mysql://origin:password@db.example/database"
	c, err := ProtocolConfigForEngine(o, testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine == nil || c.Upstream.Port != 4002 || c.Upstream.Host != "db.example" || c.Upstream.DbName != "database" {
		t.Fatalf("wrong native config: %+v", c.Upstream)
	}
	if o.OriginURL != "http://metrics.example:4000" {
		t.Fatal("HTTP origin mutated")
	}
	before := c.RestartKey
	o.MySQL.UpstreamURL += "2"
	c, err = ProtocolConfigForEngine(o, testEngine{})
	if err != nil || before == c.RestartKey {
		t.Fatalf("upstream change not reflected in restart identity: %v", err)
	}
	adapter := NewNativeListenerAdapter(testEngine{})
	if !adapter.ServesProvider("mysql") || !adapter.ServesProvider("test-engine") || !adapter.SupportsHTTP("test-engine") || adapter.SupportsHTTP("mysql") {
		t.Fatal("provider capabilities were not kept separate")
	}
}

func TestEngineStreamStateAndAnalysis(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{Engine: testEngine{}}}
	status, warnings, err := h.originProtocolState(nil)
	if err != nil || status != 2 || warnings != 7 {
		t.Fatalf("engine state = %d/%d, %v", status, warnings, err)
	}
	parsed := parseQuery("SELECT 1")
	if a := h.analyzeQuery("SELECT 1", parsed, &upstreamSession{}, time.Now()); a.Mode != sqlanalyzer.CacheModeNone {
		t.Fatalf("nil engine analyzer must not use MySQL's analyzer: %+v", a)
	}
	if (&protocolHandler{}).analyzeQuery("SELECT 1", parsed, &upstreamSession{}, time.Now()).Mode != sqlanalyzer.CacheModeObject {
		t.Fatal("default MySQL analyzer changed")
	}
}

func TestEngineUpstreamValidation(t *testing.T) {
	for _, tt := range []struct{ name, origin, upstream, want string }{
		{"HTTP credentials stay HTTP", "https://web:secret@db.example:4000", "", "include a username"},
		{"missing host", "http:///metrics", "", "no host"},
		{"port zero", "http://db.example", "mysql://db:secret@db.example:0/public", "invalid MySQL upstream port"},
		{"port overflow", "http://db.example", "mysql://db:secret@db.example:65536/public", "invalid MySQL upstream port"},
		{"wrong override scheme", "http://db.example", "postgres://db:secret@db.example/public", "unsupported MySQL origin scheme"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := validBackendOptions()
			o.OriginURL = tt.origin
			o.MySQL = mo.New()
			o.MySQL.UpstreamURL = tt.upstream
			if _, err := ProtocolConfigForEngine(o, testEngine{}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}
