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

package engines

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	ct "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestCacheKeyGolden(t *testing.T) {
	// cache keys persist across upgrades, so any change to how they are derived
	// orphans every cached object; these pin the derived keys byte for byte
	var kb keyBuilder
	kb.add(compParam, "query", "up")
	kb.add(compHeader, "X-Tenant", "")
	kb.add(compAuth, headers.NameAuthorization, "Bearer abc")
	kb.addValues(compForm, "field", []string{"a.b", "", "c"})
	kb.add(compOverride, "statement", string(make([]byte, 200)))
	pc := &po.Options{
		Path: "/", CacheKeyParams: []string{"query", "step"},
		CacheKeyHeaders: []string{"X-Tenant"},
	}
	if err := pc.Initialize(""); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1/api?query=up&step=15&query=down&ignored=1", nil)
	r.Header.Set(headers.NameAuthorization, "Bearer client")
	r.Header.Set("X-Tenant", "t1")
	r = r.WithContext(ct.WithResources(context.Background(),
		request.NewResources(&bo.Options{Paths: po.List{pc}}, pc, nil, nil, nil, nil)))
	tests := []struct {
		name, got, want string
	}{
		{
			"path", DerivePathCacheKey("/api/v1/query", http.MethodGet, "id"),
			"61753ecdc5fe4e55376072e9892ae3fd7fbc5535b65258fb9c2a2103e8edbf02",
		},
		{
			"empty-path", DerivePathCacheKey("", "", ""),
			"b0f66adc83641586656866813fd9dd0b8ebb63796075661ba45d1aa8089e1d44",
		},
		{
			"components", kb.sum("/p", "", "cors", "extra"),
			"c618aa8931ce2bd5456e8a3b0f61f1cbaa900c6ec48351a29564d7ff5c07dfd7",
		},
		{
			"request", newProxyRequest(r, nil).DeriveCacheKey("extra"),
			"be97a55f1af88c02a9ddc462bdce59c0ffbbb5c5397284de3b164054f9083f74",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Errorf("got %q want %q", test.got, test.want)
			}
		})
	}
}

func TestVarySecondaryKeyGolden(t *testing.T) {
	h := http.Header{
		"Accept-Encoding": {"gzip", "br"},
		"Accept-Language": {""},
	}
	names := []string{"Accept", "Accept-Encoding", "Accept-Language"}
	const want = "primary.vd11211e4c4994bb15a795a99ab89716844b74b820517b05581941fbf6bb03140"
	if got := varySecondaryKey("primary", "gen1", names, h); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
