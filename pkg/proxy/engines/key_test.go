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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	ct "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	proxyurls "github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	testMultipartBoundary = `; boundary=------------------------d0509edbe55938c0`
	testMultipartBody     = `--------------------------d0509edbe55938c0
Content-Disposition: form-data; name="field1"

value1
--------------------------d0509edbe55938c0
Content-Disposition: form-data; name="field2"

value2
--------------------------d0509edbe55938c0--
`
)

const testJSONDocument = `
{
	"requestType": "query",
	"query": {
		"table": "movies",
		"fields": "eidr,title",
		"filter": "year=1979",
		"options": {
			"batchSize": 20,
			"someArray": [ "test" ],
			"booleanHere": true
		}
	},
	"field1": "value1"
}
`

func TestDeepSearch(t *testing.T) {
	var document map[string]any
	err := json.Unmarshal([]byte(testJSONDocument), &document)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		key     string
		wantVal string
		wantErr bool
	}{
		{"top-level string value", "requestType", "query", false},
		{"nested string value", "query/table", "movies", false},
		{"empty key", "", "", true},
		{"missing top-level key", "missingKey", "", true},
		{"intermediate not a map", "query/filter/nottamap", "", true},
		{"nested float value", "query/options/batchSize", "20.0000", false},
		{"nested boolean value", "query/options/booleanHere", "true", false},
		{"array terminal (unsupported)", "query/options/someArray", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := deepSearch(document, tt.key)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val != tt.wantVal {
				t.Errorf("expected %s got %s", tt.wantVal, val)
			}
		})
	}
}

func TestDeriveCacheKey(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	rpath := &po.Options{
		Path:               "/",
		CacheKeyParams:     []string{"query", "step", "time"},
		CacheKeyHeaders:    []string{},
		CacheKeyFormFields: []string{"field1"},
	}

	cfg := &bo.Options{
		Paths: po.List{rpath},
	}

	newResources := func() *request.Resources {
		return request.NewResources(cfg, cfg.Paths[0], nil, nil, nil, nil)
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr = tr.WithContext(ct.WithResources(context.Background(), newResources()))

	pr := newProxyRequest(tr, nil)
	ck := pr.DeriveCacheKey("extra")

	if ck != "6ddef55b5e18cb0ec83c063baeba757950a8961fe5378d111dc88dfb6b284d1e" {
		t.Errorf("expected %s got %s", "6ddef55b5e18cb0ec83c063baeba757950a8961fe5378d111dc88dfb6b284d1e", ck)
	}

	cfg.Paths[0].CacheKeyParams = []string{"*"}

	pr = newProxyRequest(tr, nil)
	// might need to get something into the resources
	ck = pr.DeriveCacheKey("extra")
	if ck != "d81e1837216dabf4f2f37948adefb6e26590a8789f1185c94b70c30fe8d8f132" {
		t.Errorf("expected %s got %s", "d81e1837216dabf4f2f37948adefb6e26590a8789f1185c94b70c30fe8d8f132", ck)
	}

	const expected = "413fc9503d82e6845cbbb94c1a328539d3e0dd84eaa17ebcbb8976b7a0f1a5aa"

	tr = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader("field1=value1"))
	tr = tr.WithContext(ct.WithResources(context.Background(), newResources()))
	tr.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	pr = newProxyRequest(tr, nil)
	ck = pr.DeriveCacheKey("extra")
	if ck != expected {
		t.Errorf("expected %s got %s", expected, ck)
	}

	tr = httptest.NewRequest(http.MethodPut, "http://127.0.0.1/", strings.NewReader(testMultipartBody))
	tr = tr.WithContext(ct.WithResources(context.Background(), newResources()))
	tr.Header.Set(headers.NameContentType, headers.ValueMultipartFormData+testMultipartBoundary)
	tr.Header.Set(headers.NameContentLength, strconv.Itoa(len(testMultipartBody)))
	pr = newProxyRequest(tr, nil)
	ck = pr.DeriveCacheKey("extra")
	if ck != "29f3afd85b9459b5e88d4a53bbf4d9eb97581193ea8c03be6df25e51c36ce765" {
		t.Errorf("expected %s got %s", "29f3afd85b9459b5e88d4a53bbf4d9eb97581193ea8c03be6df25e51c36ce765", ck)
	}

	_, _, tr, _, _ = tu.NewTestInstance("", nil, 0, "", nil,
		providers.ReverseProxyCacheShort, "http://127.0.0.1/", "INFO")
	tr.Method = http.MethodPost
	tr.Body = io.NopCloser(strings.NewReader(testJSONDocument))
	tr = tr.WithContext(ct.WithResources(context.Background(), newResources()))
	tr.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	tr.Header.Set(headers.NameContentLength, strconv.Itoa(len(testJSONDocument)))
	pr = newProxyRequest(tr, nil)

	ck = pr.DeriveCacheKey("extra")
	if ck != "4fc96fcea5718b03be6d03eb07cbc2ae92e77a4a6d01941bb8ec7a8be904efb0" {
		t.Errorf("expected %s got %s", "4fc96fcea5718b03be6d03eb07cbc2ae92e77a4a6d01941bb8ec7a8be904efb0", ck)
	}

	// Test Custom KeyHasher Integration
	rpath.KeyHasher = exampleKeyHasher
	ck = pr.DeriveCacheKey("extra")
	if ck != "test-key" {
		t.Errorf("expected %s got %s", "test-key", ck)
	}

	tr = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
	tr.Body = io.NopCloser(strings.NewReader(testJSONDocument))
	tr = tr.WithContext(ct.WithResources(context.Background(), newResources()))
	tr.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	tr.Header.Set(headers.NameContentLength, strconv.Itoa(len(testJSONDocument)))
	pr = newProxyRequest(tr, nil)
	pr.upstreamRequest.URL = nil
	ck = pr.DeriveCacheKey("extra")
	if ck != "test-key" {
		t.Errorf("expected %s got %s", expected, ck)
	}
}

func TestDeriveCacheKeySeparatesRewrittenUpstreams(t *testing.T) {
	path := po.New()
	path.CacheKeyParams = []string{"query"}

	derive := func(host string, rewritten bool) string {
		t.Helper()
		rsc := request.NewResources(&bo.Options{
			Scheme: "http",
			Host:   "origin.example.com:9090",
		}, path, nil, nil, nil, nil)
		r := httptest.NewRequest(http.MethodGet,
			"http://"+host+"/data?query=value", nil)
		r = request.SetResources(r, rsc)
		if rewritten {
			proxyurls.SetUpstreamHost(r, host)
		}
		return newProxyRequest(r, nil).DeriveCacheKey("")
	}

	unmarkedA := derive("one.example.com", false)
	unmarkedB := derive("two.example.com", false)
	if unmarkedA != unmarkedB {
		t.Errorf("inbound hosts unexpectedly changed cache key: %s != %s", unmarkedA, unmarkedB)
	}

	markedA := derive("one.example.com", true)
	markedB := derive("two.example.com", true)
	if markedA == markedB {
		t.Errorf("rewritten upstreams share cache key %s", markedA)
	}
	if markedA == unmarkedA {
		t.Errorf("rewritten and default upstream share cache key %s", markedA)
	}
	if got := derive("one.example.com", true); got != markedA {
		t.Errorf("same rewritten upstream produced unstable keys: %s != %s", got, markedA)
	}

	path.KeyHasher = exampleKeyHasher
	customA := derive("one.example.com", true)
	customB := derive("two.example.com", true)
	if customA == customB {
		t.Errorf("custom hasher reused a key across rewritten upstreams: %s", customA)
	}
	if got := derive("one.example.com", false); got != "test-key" {
		t.Errorf("unrewritten custom key = %q, want %q", got, "test-key")
	}

	deriveWithoutPath := func(host string) string {
		rsc := request.NewResources(&bo.Options{
			Scheme: "http",
			Host:   "origin.example.com:9090",
		}, nil, nil, nil, nil, nil)
		r := httptest.NewRequest(http.MethodGet, "http://frontend.example.com/data", nil)
		r = request.SetResources(r, rsc)
		proxyurls.SetUpstreamHost(r, host)
		return newProxyRequest(r, nil).DeriveCacheKey("")
	}
	if first, second := deriveWithoutPath("one.example.com"),
		deriveWithoutPath("two.example.com"); first == second {
		t.Errorf("nil path config reused a key across rewritten upstreams: %s", first)
	}
}

func TestDeriveCacheKeyUsesFinalRewrittenUpstream(t *testing.T) {
	path := po.New()
	path.CacheKeyParams = []string{"query"}
	backendOptions := &bo.Options{
		Scheme: "http",
		Host:   "origin.example.com:9090",
	}

	derive := func(frontendURL string, rewrite func(*http.Request)) string {
		t.Helper()
		rsc := request.NewResources(backendOptions, path, nil, nil, nil, nil)
		r := httptest.NewRequest(http.MethodGet, frontendURL+"/data?query=value", nil)
		r = request.SetResources(r, rsc)
		if rewrite != nil {
			rewrite(r)
		}
		return newProxyRequest(r, nil).DeriveCacheKey("")
	}

	setTenant := func(r *http.Request) {
		proxyurls.SetUpstreamHostname(r, "tenant.example.com")
	}
	first := derive("http://frontend.example.com:8480", setTenant)
	second := derive("https://other-frontend.example.com:443", setTenant)
	if first != second {
		t.Errorf("same final upstream produced different keys: %s != %s", first, second)
	}

	defaultKey := derive("http://frontend.example.com:8480", nil)
	noOpRewrite := derive("http://frontend.example.com:8480", func(r *http.Request) {
		proxyurls.SetUpstreamHost(r, backendOptions.Host)
	})
	if defaultKey != noOpRewrite {
		t.Errorf("no-op upstream rewrite changed key: %s != %s", defaultKey, noOpRewrite)
	}
}

func TestDeriveCacheKeyUsesCanonicalTimeRangeQuery(t *testing.T) {
	canonical := func(tenant string) string {
		return "SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE tenant = '" + tenant +
			"' AND ts >= <$TS1$> AND ts < <$TS2$> GROUP BY t"
	}
	derive := func(original, identity string) string {
		t.Helper()
		path := po.New()
		path.CacheKeyParams = []string{"query"}
		rsc := request.NewResources(&bo.Options{}, path, nil, nil, nil, nil)
		rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{
			CacheKeyElements: map[string]string{"query": identity},
		}
		r := httptest.NewRequest(http.MethodGet, "http://trickster.example.com/?query="+url.QueryEscape(original), nil)
		r = request.SetResources(r, rsc)
		return newProxyRequest(r, nil).DeriveCacheKey("")
	}

	first := derive("SELECT ... WHERE tenant = 'a' AND ts >= 100 AND ts < 200", canonical("a"))
	second := derive("SELECT ... WHERE tenant = 'a' AND ts >= 300 AND ts < 400", canonical("a"))
	if first != second {
		t.Errorf("different time ranges produced different keys: %s != %s", first, second)
	}
	third := derive("SELECT ... WHERE tenant = 'b' AND ts >= 100 AND ts < 200", canonical("b"))
	if first == third {
		t.Errorf("different non-time predicates produced the same key: %s", first)
	}
}

func exampleKeyHasher(path string, params url.Values, headers http.Header,
	body []byte, trq *timeseries.TimeRangeQuery, extra string,
) string {
	return "test-key"
}

func TestDeriveCacheKeyVariesByCORSOrigin(t *testing.T) {
	makeKey := func(policy *corso.Options, origin string, customHasher bool) string {
		t.Helper()
		pc := po.New()
		if customHasher {
			pc.KeyHasher = exampleKeyHasher
		}
		rsc := request.NewResources(&bo.Options{}, pc, nil, nil, nil, nil)
		rsc.FrontendCORS = policy
		r := httptest.NewRequest(http.MethodGet, "http://trickster.example.com/data", nil)
		r.Header.Set(headers.NameOrigin, origin)
		r = request.SetResources(r, rsc)
		return newProxyRequest(r, nil).DeriveCacheKey("")
	}

	tests := []struct {
		name          string
		policy        *corso.Options
		customHasher  bool
		wantDifferent bool
	}{
		{name: "preserve", policy: &corso.Options{Mode: corso.ModePreserve}, wantDifferent: true},
		{name: "merge", policy: &corso.Options{Mode: corso.ModeMerge}, wantDifferent: true},
		{name: "replace", policy: &corso.Options{Mode: corso.ModeReplace}},
		{name: "disable", policy: &corso.Options{Mode: corso.ModeDisable}},
		{name: "legacy", policy: corso.Legacy()},
		{name: "custom hasher preserve", policy: &corso.Options{Mode: corso.ModePreserve},
			customHasher: true, wantDifferent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := makeKey(tc.policy, "https://first.example.com", tc.customHasher)
			second := makeKey(tc.policy, "https://second.example.com", tc.customHasher)
			if got := first != second; got != tc.wantDifferent {
				t.Fatalf("cache keys differ = %v, want %v (%q, %q)",
					got, tc.wantDifferent, first, second)
			}
		})
	}
}

// TestDeriveCacheKey_MultiValueParams is a comprehensive test for multi-value
// query parameter handling in cache key derivation.
// Regression tests for https://github.com/trickstercache/trickster/issues/858
func TestDeriveCacheKey_MultiValueParams(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	makeKey := func(path string, ckp []string, rawURL string) string {
		t.Helper()
		pc := &po.Options{Path: path, CacheKeyParams: ckp}
		cfg := &bo.Options{Paths: po.List{pc}}
		rsc := request.NewResources(cfg, cfg.Paths[0], nil, nil, nil, nil)
		r := httptest.NewRequest("GET", rawURL, nil)
		r = r.WithContext(ct.WithResources(context.Background(), rsc))
		return newProxyRequest(r, nil).DeriveCacheKey("")
	}

	t.Run("empty CacheKeyParams ignores all params", func(t *testing.T) {
		// This was the root cause of #858: label endpoints had empty
		// CacheKeyParams so different match[] filters shared one cache entry.
		k1 := makeKey("/api/v1/label/job/values", []string{},
			`http://h/api/v1/label/job/values?match[]={__name__="a"}`)
		k2 := makeKey("/api/v1/label/job/values", []string{},
			`http://h/api/v1/label/job/values?match[]={__name__="b"}`)
		if k1 != k2 {
			t.Error("empty CacheKeyParams should ignore query params")
		}
	})

	t.Run("label endpoint different match selectors", func(t *testing.T) {
		ckp := []string{"match[]", "start", "end"}
		k1 := makeKey("/api/v1/label/job/values", ckp,
			`http://h/api/v1/label/job/values?match[]={__name__="vm_rows"}&start=1000&end=2000`)
		k2 := makeKey("/api/v1/label/job/values", ckp,
			`http://h/api/v1/label/job/values?match[]={__name__="node_cpu_seconds_total"}&start=1000&end=2000`)
		if k1 == k2 {
			t.Errorf("different match[] must produce different keys, both got %s", k1)
		}
	})

	t.Run("labels endpoint different match selectors", func(t *testing.T) {
		ckp := []string{"match[]", "start", "end"}
		k1 := makeKey("/api/v1/labels", ckp,
			`http://h/api/v1/labels?match[]={__name__="vm_rows"}`)
		k2 := makeKey("/api/v1/labels", ckp,
			`http://h/api/v1/labels?match[]={__name__="node_cpu"}`)
		if k1 == k2 {
			t.Errorf("different match[] must produce different keys, both got %s", k1)
		}
	})

	t.Run("different label names produce different keys", func(t *testing.T) {
		ckp := []string{"match[]", "start", "end"}
		k1 := makeKey("/api/v1/label/job/values", ckp,
			`http://h/api/v1/label/job/values?match[]={__name__="up"}`)
		k2 := makeKey("/api/v1/label/instance/values", ckp,
			`http://h/api/v1/label/instance/values?match[]={__name__="up"}`)
		if k1 == k2 {
			t.Errorf("different label paths must produce different keys, both got %s", k1)
		}
	})

	t.Run("multi-value match vs single-value match", func(t *testing.T) {
		ckp := []string{"match[]", "start", "end"}
		k1 := makeKey("/api/v1/series", ckp,
			`http://h/api/v1/series?match[]={__name__="up"}&match[]={__name__="down"}&start=0&end=0`)
		k2 := makeKey("/api/v1/series", ckp,
			`http://h/api/v1/series?match[]={__name__="up"}&start=0&end=0`)
		if k1 == k2 {
			t.Errorf("different match[] count must produce different keys, both got %s", k1)
		}
	})

	t.Run("wildcard CacheKeyParams includes all multi-value params", func(t *testing.T) {
		k1 := makeKey("/api/v1/series", []string{"*"},
			`http://h/api/v1/series?match[]={__name__="up"}&match[]={__name__="down"}&start=0&end=0`)
		k2 := makeKey("/api/v1/series", []string{"*"},
			`http://h/api/v1/series?match[]={__name__="up"}&start=0&end=0`)
		if k1 == k2 {
			t.Errorf("wildcard mode: different match[] count must produce different keys, both got %s", k1)
		}
	})

	t.Run("single-value params unchanged", func(t *testing.T) {
		// Ensure the multi-value change doesn't alter keys for single-value params.
		// This uses the same config as TestDeriveCacheKey to confirm stability.
		rpath := &po.Options{
			Path:           "/",
			CacheKeyParams: []string{"query", "step", "time"},
		}
		cfg := &bo.Options{Paths: po.List{rpath}}
		rsc := request.NewResources(cfg, cfg.Paths[0], nil, nil, nil, nil)
		r := httptest.NewRequest("GET",
			"http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
		r = r.WithContext(ct.WithResources(context.Background(), rsc))
		k := newProxyRequest(r, nil).DeriveCacheKey("extra")
		if k != "6ddef55b5e18cb0ec83c063baeba757950a8961fe5378d111dc88dfb6b284d1e" {
			t.Errorf("single-value param key changed: got %s, want 6ddef55b5e18cb0ec83c063baeba757950a8961fe5378d111dc88dfb6b284d1e", k)
		}
	})

	t.Run("no match param vs with match param", func(t *testing.T) {
		ckp := []string{"match[]", "start", "end"}
		k1 := makeKey("/api/v1/label/job/values", ckp,
			`http://h/api/v1/label/job/values?start=1000&end=2000`)
		k2 := makeKey("/api/v1/label/job/values", ckp,
			`http://h/api/v1/label/job/values?match[]={__name__="up"}&start=1000&end=2000`)
		if k1 == k2 {
			t.Errorf("presence vs absence of match[] must produce different keys, both got %s", k1)
		}
	})
}

func TestDeriveCacheKeyAuthHeader(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	client, err := NewTestClient("test", &bo.Options{
		Paths: po.List{{
			Path:            "/",
			CacheKeyParams:  []string{"query", "step", "time"},
			CacheKeyHeaders: []string{"X-Test-Header"},
		}},
	}, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr = tr.WithContext(ct.WithResources(context.Background(),
		request.NewResources(client.Configuration(), client.Configuration().Paths[0],
			nil, nil, nil, nil)))

	tr.Header.Add("Authorization", "test")
	tr.Header.Add("X-Test-Header", "test2")

	pr := newProxyRequest(tr, nil)

	ck := pr.DeriveCacheKey("extra")

	if ck != "49fb4e15c9e560b0e022d8c3093504ac7b2ce145292fcc945ca2aecab2f10d05" {
		t.Errorf("expected %s got %s", "49fb4e15c9e560b0e022d8c3093504ac7b2ce145292fcc945ca2aecab2f10d05", ck)
	}
}

func TestDeriveCacheKeyNoPathConfig(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	client, err := NewTestClient("test", &bo.Options{
		Paths: po.List{{
			Path:            "/",
			CacheKeyParams:  []string{"query", "step", "time"},
			CacheKeyHeaders: []string{},
		}},
	}, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr = tr.WithContext(ct.WithResources(context.Background(),
		request.NewResources(client.Configuration(), nil, nil, nil, nil, nil)))

	pr := newProxyRequest(tr, nil)
	ck := pr.DeriveCacheKey("extra")

	if ck != "e23c350d0d237d66d22b644a9ac894a3a776e72478ac3c351c6f75c34d5a9b1d" {
		t.Errorf("expected %s got %s", "e23c350d0d237d66d22b644a9ac894a3a776e72478ac3c351c6f75c34d5a9b1d", ck)
	}
}

func TestDeriveCacheKeyNilURL(t *testing.T) {
	_, w, r, _, _ := tu.NewTestInstance("", nil, 0, "", nil, providers.ReverseProxyCacheShort,
		"http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", "INFO")

	pr := newProxyRequest(r, w)
	pr.upstreamRequest.URL = nil
	k := pr.DeriveCacheKey("")
	if k != "1de65ee1a222eb17a15867a8fa29c94f9a995bc4e0d47671c07953941427901f" {
		t.Errorf("unexpected cache key: %s", k)
	}
}

// TestCacheKey_BackendNamePrefixIsolatesPoolMembers asserts that two backends
// sharing CacheKeyPrefix and the same cache produce distinct cache keys per
// engine, with the backend name as the leading segment.
func TestCacheKey_BackendNamePrefixIsolatesPoolMembers(t *testing.T) {
	const sharedPrefix = "shared"
	derived := "abc123"

	cases := []struct {
		engine string
		want   func(name string) string
	}{
		{"opc", func(n string) string { return n + "." + sharedPrefix + ".opc." + derived }},
		{"dpc", func(n string) string { return n + "." + sharedPrefix + ".dpc." + derived }},
		{"http", func(n string) string { return n + "." + sharedPrefix + "." + derived }},
	}

	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			a := &bo.Options{Name: "a", CacheKeyPrefix: sharedPrefix}
			b := &bo.Options{Name: "b", CacheKeyPrefix: sharedPrefix}

			ka := composeKey(tc.engine, a, derived)
			kb := composeKey(tc.engine, b, derived)

			if ka == kb {
				t.Fatalf("backends %q and %q produced colliding key %q", a.Name, b.Name, ka)
			}
			if !strings.HasPrefix(ka, "a.") {
				t.Errorf("expected key to start with %q, got %q", "a.", ka)
			}
			if !strings.HasPrefix(kb, "b.") {
				t.Errorf("expected key to start with %q, got %q", "b.", kb)
			}
			if ka != tc.want("a") {
				t.Errorf("unexpected key for backend a: got %q want %q", ka, tc.want("a"))
			}
			if kb != tc.want("b") {
				t.Errorf("unexpected key for backend b: got %q want %q", kb, tc.want("b"))
			}
		})
	}
}

// composeKey mirrors the per-engine cache key composition. Keep this in sync
// with the call sites in objectproxycache.go, deltaproxycache.go, httpproxy.go
// and the purge handler in pkg/proxy/handlers/trickster/purge/purge.go.
func composeKey(engine string, o *bo.Options, derived string) string {
	switch engine {
	case "opc":
		return o.Name + "." + o.CacheKeyPrefix + ".opc." + derived
	case "dpc":
		return o.Name + "." + o.CacheKeyPrefix + ".dpc." + derived
	case "http":
		return o.Name + "." + o.CacheKeyPrefix + "." + derived
	}
	return ""
}

func TestDeriveCacheKeyEffectiveIdentity(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	newPR := func(pc *po.Options, auth string) *proxyRequest {
		cfg := &bo.Options{Paths: po.List{pc}}
		tr := httptest.NewRequest("GET", "http://127.0.0.1/render?target=a.b", nil)
		if auth != "" {
			tr.Header.Set(headers.NameAuthorization, auth)
		}
		tr = tr.WithContext(ct.WithResources(context.Background(),
			request.NewResources(cfg, pc, nil, nil, nil, nil)))
		return newProxyRequest(tr, nil)
	}
	path := func(hdrs, params map[string]string) *po.Options {
		return &po.Options{Path: "/render", CacheKeyParams: []string{"target"},
			RequestHeaders: hdrs, RequestParams: params}
	}

	// rotating a pinned credential rotates the key, with no inbound auth at
	// all — the metadata-route and late-fallback shape
	kA := newPR(path(map[string]string{"Authorization": "Bearer tenant-a"}, nil), "").DeriveCacheKey("")
	kB := newPR(path(map[string]string{"Authorization": "Bearer tenant-b"}, nil), "").DeriveCacheKey("")
	if kA == kB {
		t.Error("a rotated pinned credential must change the cache key")
	}

	// with a static override, the discarded inbound value does not fragment
	// the key, but the configured replacement is represented in it
	pcPinned := path(map[string]string{"Authorization": "Bearer tenant-a"}, nil)
	k1 := newPR(pcPinned, "Bearer client-1").DeriveCacheKey("")
	k2 := newPR(pcPinned, "Bearer client-2").DeriveCacheKey("")
	if k1 != k2 {
		t.Error("clients behind one pinned identity must share a key")
	}
	if k1 != kA {
		t.Error("the pinned identity itself must be part of the key")
	}

	// without an override, inbound identities stay separated
	plain := path(nil, nil)
	if newPR(plain, "Bearer c1").DeriveCacheKey("") == newPR(plain, "Bearer c2").DeriveCacheKey("") {
		t.Error("distinct inbound identities must not share a key")
	}

	// configured request_params are identity too (a clustered view selector)
	pA := newPR(path(nil, map[string]string{"local": "1"}), "").DeriveCacheKey("")
	pB := newPR(path(nil, map[string]string{"local": "0"}), "").DeriveCacheKey("")
	if pA == pB {
		t.Error("a rotated configured parameter must change the cache key")
	}
}

func TestDeriveCacheKeyUnambiguousEncoding(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	newPR := func(pc *po.Options, hdrs map[string]string, target string) *proxyRequest {
		cfg := &bo.Options{Paths: po.List{pc}}
		tr := httptest.NewRequest("GET", target, nil)
		for k, v := range hdrs {
			tr.Header.Set(k, v)
		}
		tr = tr.WithContext(ct.WithResources(context.Background(),
			request.NewResources(cfg, pc, nil, nil, nil, nil)))
		return newProxyRequest(tr, nil)
	}
	path := func(hdrs, params map[string]string) *po.Options {
		return &po.Options{Path: "/render", CacheKeyParams: []string{"target"},
			RequestHeaders: hdrs, RequestParams: params}
	}
	const u = "http://127.0.0.1/render?target=a.b"

	// configured header pairs that concatenate identically must not collide:
	// {X: Y.Z} and {X.Y: Z} are different effective upstream identities
	k1 := newPR(path(map[string]string{"X": "Y.Z"}, nil), nil, u).DeriveCacheKey("")
	k2 := newPR(path(map[string]string{"X.Y": "Z"}, nil), nil, u).DeriveCacheKey("")
	if k1 == k2 {
		t.Error("dotted configured header identities must not share a cache key")
	}

	// the analogous configured request-parameter case
	p1 := newPR(path(nil, map[string]string{"x": "y.z"}), nil, u).DeriveCacheKey("")
	p2 := newPR(path(nil, map[string]string{"x.y": "z"}), nil, u).DeriveCacheKey("")
	if p1 == p2 {
		t.Error("dotted configured parameter identities must not share a cache key")
	}

	// a configured header pair and an identical configured parameter pair are
	// different identities
	if newPR(path(map[string]string{"X": "1"}, nil), nil, u).DeriveCacheKey("") ==
		newPR(path(nil, map[string]string{"X": "1"}), nil, u).DeriveCacheKey("") {
		t.Error("configured header and parameter identities must be typed apart")
	}

	// client-supplied elements have the same property: a keyed header and a
	// keyed parameter with one name and value must not collide
	hp := &po.Options{Path: "/render", CacheKeyParams: []string{"token"},
		CacheKeyHeaders: []string{"Token"}}
	viaParam := newPR(hp, nil, "http://127.0.0.1/render?token=abc").DeriveCacheKey("")
	viaHeader := newPR(hp, map[string]string{"Token": "abc"},
		"http://127.0.0.1/render").DeriveCacheKey("")
	if viaParam == viaHeader {
		t.Error("a keyed parameter and a keyed header must be typed apart")
	}

	// value boundaries are length-prefixed: name/value splits of one
	// concatenation are distinct keys
	if newPR(hp, nil, "http://127.0.0.1/render?token=ab").DeriveCacheKey("") ==
		newPR(hp, nil, "http://127.0.0.1/render?token=a.b").DeriveCacheKey("") {
		t.Error("parameter values must be length-prefixed, not delimiter-joined")
	}
}

func TestDeriveCacheKeyEffectiveValues(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	newPR := func(pc *po.Options, hdrs map[string]string, target string) *proxyRequest {
		cfg := &bo.Options{Paths: po.List{pc}}
		tr := httptest.NewRequest("GET", target, nil)
		for k, v := range hdrs {
			tr.Header.Set(k, v)
		}
		tr = tr.WithContext(ct.WithResources(context.Background(),
			request.NewResources(cfg, pc, nil, nil, nil, nil)))
		return newProxyRequest(tr, nil)
	}
	renderURL := func(local string) string {
		if local == "" {
			return "http://127.0.0.1/render?target=a.b"
		}
		return "http://127.0.0.1/render?target=a.b&local=" + local
	}

	t.Run("replaced param does not fragment", func(t *testing.T) {
		pc := &po.Options{Path: "/render", CacheKeyParams: []string{"target", "local"},
			RequestParams: map[string]string{"local": "1"}}
		keys := map[string]bool{}
		for _, v := range []string{"", "0", "1", "junk", "aaaaaaaa"} {
			keys[newPR(pc, nil, renderURL(v)).DeriveCacheKey("")] = true
		}
		if len(keys) != 1 {
			t.Errorf("clients behind a pinned parameter must share one cache key, got %d", len(keys))
		}
	})

	t.Run("removed param does not fragment", func(t *testing.T) {
		pc := &po.Options{Path: "/render", CacheKeyParams: []string{"target", "local"},
			RequestParams: map[string]string{"-local": ""}}
		if newPR(pc, nil, renderURL("0")).DeriveCacheKey("") !=
			newPR(pc, nil, renderURL("5")).DeriveCacheKey("") {
			t.Error("clients behind a removed parameter must share one cache key")
		}
	})

	t.Run("appended param keeps the client component", func(t *testing.T) {
		pc := &po.Options{Path: "/render", CacheKeyParams: []string{"target", "local"},
			RequestParams: map[string]string{"+local": "1"}}
		if newPR(pc, nil, renderURL("0")).DeriveCacheKey("") ==
			newPR(pc, nil, renderURL("5")).DeriveCacheKey("") {
			t.Error("an appended parameter must still key the client's value")
		}
	})

	t.Run("wildcard params honor replacement", func(t *testing.T) {
		pc := &po.Options{Path: "/render", CacheKeyParams: []string{"*"},
			RequestParams: map[string]string{"local": "1"}}
		if newPR(pc, nil, renderURL("0")).DeriveCacheKey("") !=
			newPR(pc, nil, renderURL("5")).DeriveCacheKey("") {
			t.Error("wildcard cache_key_params must key the effective, not inbound, value")
		}
	})

	t.Run("replaced cache_key_header does not fragment", func(t *testing.T) {
		pc := &po.Options{Path: "/render", CacheKeyParams: []string{"target"},
			CacheKeyHeaders: []string{"X-Tenant"},
			RequestHeaders:  map[string]string{"X-Tenant": "shared"}}
		k1 := newPR(pc, map[string]string{"X-Tenant": "t1"}, renderURL("")).DeriveCacheKey("")
		k2 := newPR(pc, map[string]string{"X-Tenant": "t2"}, renderURL("")).DeriveCacheKey("")
		if k1 != k2 {
			t.Error("clients behind a pinned cache_key_header must share one cache key")
		}
		// and without the pin they stay separated
		plain := &po.Options{Path: "/render", CacheKeyParams: []string{"target"},
			CacheKeyHeaders: []string{"X-Tenant"}}
		if newPR(plain, map[string]string{"X-Tenant": "t1"}, renderURL("")).DeriveCacheKey("") ==
			newPR(plain, map[string]string{"X-Tenant": "t2"}, renderURL("")).DeriveCacheKey("") {
			t.Error("distinct tenants must not share a key absent an override")
		}
	})

	t.Run("replaced form field does not fragment", func(t *testing.T) {
		pc := &po.Options{Path: "/render", CacheKeyParams: []string{},
			CacheKeyFormFields: []string{"local"},
			RequestParams:      map[string]string{"local": "1"}}
		post := func(body string) *proxyRequest {
			cfg := &bo.Options{Paths: po.List{pc}}
			tr := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/render",
				strings.NewReader(body))
			tr.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
			tr.Header.Set(headers.NameContentLength, strconv.Itoa(len(body)))
			tr = tr.WithContext(ct.WithResources(context.Background(),
				request.NewResources(cfg, pc, nil, nil, nil, nil)))
			return newProxyRequest(tr, nil)
		}
		if post(`{"local": "0"}`).DeriveCacheKey("") !=
			post(`{"local": "5"}`).DeriveCacheKey("") {
			t.Error("clients behind a pinned form field must share one cache key")
		}
	})
}
