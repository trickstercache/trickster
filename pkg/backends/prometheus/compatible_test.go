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

package prometheus

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestSupportedPathsIsolation(t *testing.T) {
	o := bo.New()
	original := SupportedPaths(o)
	if o.FastForwardPath != nil {
		t.Fatal("path catalogue must not mutate backend options")
	}
	changed := WithPathPrefix(WithCacheKeyHeaders(WithCacheKeyParams(original, "db", "query"), "x-greptime-db-name"), "/v1/prometheus/")
	changed[0].Methods[0] = "PATCH"
	changed[1].ResponseHeaders[headers.NameCacheControl] = "private"
	if original[0].Path != "/api/v1/query_range" || original[0].Methods[0] != http.MethodGet || slices.Contains(original[0].CacheKeyParams, "db") {
		t.Fatal("route transformation mutated its input")
	}
	if changed[0].Path != "/v1/prometheus/api/v1/query_range" || len(changed[0].CacheKeyParams) != len(original[0].CacheKeyParams)+1 {
		t.Fatal("invalid prefix or duplicate cache key parameter")
	}
	if changed[2].ResponseHeaders[headers.NameCacheControl] == "private" || original[1].ResponseHeaders[headers.NameCacheControl] == "private" {
		t.Fatal("route response maps alias each other")
	}
	filtered := Without(changed, "alerts", "admin", "proxycache", "proxy")
	if len(filtered) != 5 || len(changed) != len(original) {
		t.Fatalf("wrong supported subset: %d", len(filtered))
	}
	filtered[0].CacheKeyHeaders[0] = "changed"
	if changed[0].CacheKeyHeaders[0] == "changed" || SupportedPaths(nil)[1].ResponseHeaders[headers.NameCacheControl] == "private" {
		t.Fatal("default routes share mutable state")
	}
}

func TestCompatibleHandlerLookup(t *testing.T) {
	called := 0
	c, err := NewClientWithHooks("compatible", nil, nil, nil, Hooks{
		PathPrefix:     "/prefix",
		PrepareRequest: func(*http.Request) bool { called++; return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := c.HandlerLookup()
	lookup["admin"].ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/prefix/api/v1/admin", nil))
	if called != 1 || !slices.Contains(c.MergeablePaths(), "/prefix/api/v1/query_range") {
		t.Fatal("embedding hooks not applied")
	}
	delete(lookup, "query")
	if c.HandlerLookup()["query"] == nil {
		t.Fatal("handler lookup shares mutable map")
	}
}

func TestCompatibleHealthAndOptionIsolation(t *testing.T) {
	o := bo.New()
	o.OriginURL = "http://example.test/base"
	hooks := Hooks{PathPrefix: "/prefix", CacheKeyParams: []string{"db"}, CacheKeyHeaders: []string{"X-Database"}}
	c, err := NewClientWithHooks("compatible", o, nil, nil, hooks)
	if err != nil {
		t.Fatal(err)
	}
	c.BaseUpstreamURL().Path = "/base"
	hooks.CacheKeyParams[0] = "changed"
	hooks.CacheKeyHeaders[0] = "Changed"
	paths := c.DefaultPathConfigs(o)
	if !slices.Contains(paths[0].CacheKeyParams, "db") || !slices.Contains(paths[0].CacheKeyHeaders, "X-Database") {
		t.Fatal("constructor retained mutable option slices")
	}
	if c.DefaultHealthCheckConfig().Path != "/base/prefix/api/v1/query" {
		t.Fatalf("unexpected probe: %+v", c.DefaultHealthCheckConfig())
	}
	c.hooks.HealthCheckConfig = func(u *url.URL) *ho.Options { h := ho.New(); h.Path = u.Path + "/health"; return h }
	if c.DefaultHealthCheckConfig().Path != "/base/health" {
		t.Fatal("probe hook not called")
	}
}
