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

package request

import (
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"go.opentelemetry.io/otel/attribute"
)

func TestTracingAttributes(t *testing.T) {
	rsc := &Resources{
		BackendOptions: &bo.Options{Name: "origin-a", Provider: "prometheus"},
		CacheConfig:    &co.Options{Name: "memory-cache", Provider: "memory"},
		PathConfig:     &po.Options{Path: "/api/v1/query", HandlerName: "proxycache"},
	}

	got := tracingAttributesMap(rsc.TracingAttributes())
	want := map[string]string{
		"backend.name":     "origin-a",
		"backend.provider": "prometheus",
		"cache.name":       "memory-cache",
		"cache.provider":   "memory",
		"router.path":      "/api/v1/query",
		"router.handler":   "proxycache",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("attribute %q: expected %q, got %q", k, v, got[k])
		}
	}
}

func TestTracingAttributesSkipsMissingResources(t *testing.T) {
	rsc := &Resources{
		BackendOptions: &bo.Options{Name: "origin-a"},
		PathConfig:     &po.Options{Path: "/api/v1/query"},
	}

	got := tracingAttributesMap(rsc.TracingAttributes())
	if got["backend.name"] != "origin-a" {
		t.Fatalf("expected backend.name, got %q", got["backend.name"])
	}
	if got["router.path"] != "/api/v1/query" {
		t.Fatalf("expected router.path, got %q", got["router.path"])
	}
	if _, ok := got["cache.name"]; ok {
		t.Fatal("cache.name must not be set when cache config is nil")
	}
	if attrs := (*Resources)(nil).TracingAttributes(); len(attrs) != 0 {
		t.Fatalf("nil resources must return no attributes, got %d", len(attrs))
	}
}

func tracingAttributesMap(attrs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}
