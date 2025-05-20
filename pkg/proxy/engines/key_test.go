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
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testMultipartBoundary = `; boundary=------------------------d0509edbe55938c0`
const testMultipartBody = `--------------------------d0509edbe55938c0
Content-Disposition: form-data; name="field1"

value1
--------------------------d0509edbe55938c0
Content-Disposition: form-data; name="field2"

value2
--------------------------d0509edbe55938c0--
`

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
		t.Error(err)
	}

	val, err := deepSearch(document, "query/table")
	if err != nil {
		t.Error(err)
	}

	if val != "movies" {
		t.Errorf("expected %s got %s", "movies", val)
	}

	_, err = deepSearch(document, "")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}

	_, err = deepSearch(document, "missingKey")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}

	_, err = deepSearch(document, "query/filter/nottamap")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}

	_, err = deepSearch(document, "query/options/batchSize")
	if err != nil {
		t.Error(err)
	}

	_, err = deepSearch(document, "query/options/booleanHere")
	if err != nil {
		t.Error(err)
	}

	_, err = deepSearch(document, "query/options/someArray")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
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
		Paths: po.Lookup{
			"root": rpath,
		},
	}

	newResources := func() *request.Resources {
		return request.NewResources(cfg, cfg.Paths["root"], nil, nil, nil, nil)
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr = tr.WithContext(ct.WithResources(context.Background(), newResources()))

	pr := newProxyRequest(tr, nil)
	ck := pr.DeriveCacheKey("extra")

	if ck != "52dc11456c84506d3444e53ee4c99777" {
		t.Errorf("expected %s got %s", "52dc11456c84506d3444e53ee4c99777", ck)
	}

	cfg.Paths["root"].CacheKeyParams = []string{"*"}

	pr = newProxyRequest(tr, nil)
	// might need to get something into the resources
	ck = pr.DeriveCacheKey("extra")
	if ck != "407aba34f02c87f6898a6d80b01f38a4" {
		t.Errorf("expected %s got %s", "407aba34f02c87f6898a6d80b01f38a4", ck)
	}

	const expected = "cb84ad010abb4d0f864470540a46f137"

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
	if ck != "1caeb9fb60f5613ed89d68d5ab8bed99" {
		t.Errorf("expected %s got %s", "1caeb9fb60f5613ed89d68d5ab8bed99", ck)
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
	if ck != "82c1d86126a02b96b8d0fcb94a9f486a" {
		t.Errorf("expected %s got %s", "82c1d86126a02b96b8d0fcb94a9f486a", ck)
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

func exampleKeyHasher(path string, params url.Values, headers http.Header,
	body []byte, trq *timeseries.TimeRangeQuery, extra string) string {
	return "test-key"
}

func TestDeriveCacheKeyAuthHeader(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	client, err := NewTestClient("test", &bo.Options{
		Paths: po.Lookup{
			"root": {
				Path:            "/",
				CacheKeyParams:  []string{"query", "step", "time"},
				CacheKeyHeaders: []string{"X-Test-Header"},
			},
		},
	}, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr = tr.WithContext(ct.WithResources(context.Background(),
		request.NewResources(client.Configuration(), client.Configuration().Paths["root"],
			nil, nil, nil, nil)))

	tr.Header.Add("Authorization", "test")
	tr.Header.Add("X-Test-Header", "test2")

	pr := newProxyRequest(tr, nil)

	ck := pr.DeriveCacheKey("extra")

	if ck != "60257fa6b18d6072b90a294269a8e6e1" {
		t.Errorf("expected %s got %s", "60257fa6b18d6072b90a294269a8e6e1", ck)
	}

}

func TestDeriveCacheKeyNoPathConfig(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	client, err := NewTestClient("test", &bo.Options{
		Paths: po.Lookup{
			"root": {
				Path:            "/",
				CacheKeyParams:  []string{"query", "step", "time"},
				CacheKeyHeaders: []string{},
			},
		},
	}, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", nil)
	tr = tr.WithContext(ct.WithResources(context.Background(),
		request.NewResources(client.Configuration(), nil, nil, nil, nil, nil)))

	pr := newProxyRequest(tr, nil)
	ck := pr.DeriveCacheKey("extra")

	if ck != "f53b04ce5c434a7357804ae15a64ee6c" {
		t.Errorf("expected %s got %s", "f53b04ce5c434a7357804ae15a64ee6c", ck)
	}

}

func TestDeriveCacheKeyNilURL(t *testing.T) {

	_, w, r, _, _ := tu.NewTestInstance("", nil, 0, "", nil, providers.ReverseProxyCacheShort,
		"http://127.0.0.1/?query=12345&start=0&end=0&step=300&time=0", "INFO")

	pr := newProxyRequest(r, w)
	pr.upstreamRequest.URL = nil
	k := pr.DeriveCacheKey("")
	if k != "c04284eb2c269dd939d54437d4efb071" {
		t.Errorf("unexpected cache key: %s", k)
	}
}
