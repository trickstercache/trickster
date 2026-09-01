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

package druid

import (
	"net/http"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestRegisterHandlers(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := c.Handlers()
	for _, name := range []string{handlerHealth, handlerQuery, handlerProxyCache, providers.Proxy} {
		if h[name] == nil {
			t.Errorf("handler %q is not registered", name)
		}
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := c.DefaultPathConfigs(nil)
	if len(paths) != 6 {
		t.Fatalf("got %d paths, want 6", len(paths))
	}
	for _, p := range paths {
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		method, path, handler string
		body                  bool
	}{
		{http.MethodPost, "/druid/v2", handlerQuery, false},
		{http.MethodPost, "/druid/v2/sql", handlerProxyCache, true},
		{http.MethodPost, "/druid/v2/sql/task", providers.Proxy, false},
		{http.MethodPost, "/druid/v2/sql/task/abc", providers.Proxy, false},
		{http.MethodGet, "/druid/v2/datasources/wiki", handlerProxyCache, false},
		{http.MethodGet, "/status/health", handlerHealth, false},
		{http.MethodDelete, "/druid/indexer/v1/task/x", providers.Proxy, false},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			got := paths.Match(test.method, test.path)
			if got == nil || got.HandlerName != test.handler {
				t.Fatalf("handler = %#v, want %q", got, test.handler)
			}
			if got.CacheKeyBody != test.body {
				t.Fatalf("CacheKeyBody = %t, want %t", got.CacheKeyBody, test.body)
			}
		})
	}
	sql := paths.Match(http.MethodPost, "/druid/v2/sql")
	if sql == nil || !slices.Equal(sql.CacheKeyParams, []string{"*"}) ||
		!slices.Contains(sql.CacheKeyHeaders, headers.NameContentType) {
		t.Fatalf("SQL route cache identity is incomplete: %#v", sql)
	}
	datasources := paths.Match(http.MethodGet, "/druid/v2/datasources/wiki?full=true")
	if datasources == nil || !slices.Equal(datasources.CacheKeyParams, []string{"*"}) {
		t.Fatalf("datasources route cache identity is incomplete: %#v", datasources)
	}
}
