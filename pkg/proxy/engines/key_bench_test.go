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
	"net/http/httptest"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	ct "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// the configured identity is canonicalized once at path initialization, so
// derivation cost must not grow with the static identity field count
func benchmarkDeriveCacheKey(b *testing.B, hdrs, params map[string]string) {
	pc := &po.Options{Path: "/", CacheKeyParams: []string{"query", "step", "time"},
		RequestHeaders: hdrs, RequestParams: params}
	if err := pc.Initialize(""); err != nil {
		b.Fatal(err)
	}
	cfg := &bo.Options{Paths: po.List{pc}}
	tr := httptest.NewRequest("GET",
		"http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr.Header.Set("Authorization", "Bearer client-credential")
	tr = tr.WithContext(ct.WithResources(context.Background(),
		request.NewResources(cfg, pc, nil, nil, nil, nil)))
	pr := newProxyRequest(tr, nil)
	b.ReportAllocs()
	for b.Loop() {
		pr.DeriveCacheKey("extra")
	}
}

func BenchmarkDeriveCacheKeyStaticIdentity0(b *testing.B) {
	benchmarkDeriveCacheKey(b, nil, nil)
}

func BenchmarkDeriveCacheKeyStaticIdentity1(b *testing.B) {
	benchmarkDeriveCacheKey(b,
		map[string]string{"Authorization": "Bearer pinned-tenant"}, nil)
}

func BenchmarkDeriveCacheKeyStaticIdentity6(b *testing.B) {
	benchmarkDeriveCacheKey(b,
		map[string]string{
			"Authorization": "Bearer pinned-tenant",
			"X-Tenant":      "shared",
			"-X-Debug":      "",
		},
		map[string]string{
			"local":   "1",
			"+region": "us-east-1",
			"-trace":  "",
		})
}

// one warm request populates the cache, then every iteration is a full
// object-cache hit, key derivation included
func benchmarkObjectCacheHit(b *testing.B, hdrs, params map[string]string) {
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test",
		200, map[string]string{"Cache-Control": "max-age=3600"})
	if err != nil {
		b.Fatal(err)
	}
	defer ts.Close()
	pc := rsc.PathConfig
	pc.RequestHeaders, pc.RequestParams = hdrs, params
	if err = pc.Initialize(""); err != nil {
		b.Fatal(err)
	}
	w := httptest.NewRecorder()
	ObjectProxyCacheRequest(w, r)
	if w.Code != 200 {
		b.Fatalf("warm request returned %d", w.Code)
	}
	b.ReportAllocs()
	for b.Loop() {
		ObjectProxyCacheRequest(httptest.NewRecorder(), r)
	}
}

func BenchmarkObjectCacheHitStaticIdentity0(b *testing.B) {
	benchmarkObjectCacheHit(b, nil, nil)
}

func BenchmarkObjectCacheHitStaticIdentity4(b *testing.B) {
	benchmarkObjectCacheHit(b,
		map[string]string{"Authorization": "Bearer pinned-tenant", "X-Tenant": "shared"},
		map[string]string{"local": "1", "-trace": ""})
}
