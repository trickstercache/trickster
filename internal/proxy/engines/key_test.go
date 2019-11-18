/**
* Copyright 2018 Comcast Cable Communications Management, LLC
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

package engines

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	ct "github.com/Comcast/trickster/internal/util/context"
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

	var document map[string]interface{}
	json.Unmarshal([]byte(testJSONDocument), &document)

	val, err := deepSearch(document, "query/table")
	if err != nil {
		t.Error(err)
	}

	if val != "movies" {
		t.Errorf("expected %s got %s", "movies", val)
	}

	val, err = deepSearch(document, "")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}

	val, err = deepSearch(document, "missingKey")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}

	val, err = deepSearch(document, "query/filter/nottamap")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}

	val, err = deepSearch(document, "query/options/batchSize")
	if err != nil {
		t.Error(err)
	}

	val, err = deepSearch(document, "query/options/booleanHere")
	if err != nil {
		t.Error(err)
	}

	val, err = deepSearch(document, "query/options/someArray")
	if err == nil {
		t.Errorf("expected error: %s", "could not find key")
	}
}

func TestDeriveCacheKey(t *testing.T) {

	rpath := &config.PathConfig{
		Path:               "/",
		CacheKeyParams:     []string{"query", "step", "time"},
		CacheKeyHeaders:    []string{},
		CacheKeyFormFields: []string{"field1"},
	}

	cfg := &config.OriginConfig{
		Paths: map[string]*config.PathConfig{
			"root": rpath,
		},
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1", nil)
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), cfg, nil, cfg.Paths["root"]))

	u := &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r := &model.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}, ClientRequest: tr}
	key := DeriveCacheKey(r, nil, "extra")

	if key != "b82c27cea3f89ae33174565990e32ccb" {
		t.Errorf("expected %s got %s", "b82c27cea3f89ae33174565990e32ccb", key)
	}

	cfg.Paths["root"].CacheKeyParams = []string{"*"}

	u = &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r = &model.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}, ClientRequest: tr}
	key = DeriveCacheKey(r, nil, "extra")
	if key != "d22b4d54f7dce72faebd02a1c2cd4549" {
		t.Errorf("expected %s got %s", "d22b4d54f7dce72faebd02a1c2cd4549", key)
	}

	const expected = "3ee2473c4ea66b70680bd62616104c0a"

	tr = httptest.NewRequest(http.MethodPost, "http://127.0.0.1", bytes.NewReader([]byte("field1=value1")))
	tr.Header.Set(headers.NameContentType, headers.ValueXFormUrlEncoded)
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), cfg, nil, cfg.Paths["root"]))
	r.ClientRequest = tr
	key = DeriveCacheKey(r, nil, "extra")
	if key != expected {
		t.Errorf("expected %s got %s", expected, key)
	}

	tr = httptest.NewRequest(http.MethodPut, "http://127.0.0.1", bytes.NewReader([]byte(testMultipartBody)))
	tr.Header.Set(headers.NameContentType, headers.ValueMultipartFormData+testMultipartBoundary)
	tr.Header.Set(headers.NameContentLength, strconv.Itoa(len(testMultipartBody)))
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), cfg, nil, cfg.Paths["root"]))
	r.ClientRequest = tr
	key = DeriveCacheKey(r, nil, "extra")
	if key != expected {
		t.Errorf("expected %s got %s", expected, key)
	}

	tr = httptest.NewRequest(http.MethodPost, "http://127.0.0.1", bytes.NewReader([]byte(testJSONDocument)))
	tr.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	tr.Header.Set(headers.NameContentLength, strconv.Itoa(len(testJSONDocument)))
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), cfg, nil, cfg.Paths["root"]))
	r.ClientRequest = tr
	key = DeriveCacheKey(r, nil, "extra")
	if key != expected {
		t.Errorf("expected %s got %s", expected, key)
	}

	// Test Custom KeyHasher Integration
	rpath.KeyHasher = []config.KeyHasherFunc{exampleKeyHasher}

	key = DeriveCacheKey(r, nil, "extra")
	if key != "test-key" {
		t.Errorf("expected %s got %s", "test-key", key)
	}

}

func exampleKeyHasher(path string, params url.Values, headers http.Header, body io.ReadCloser, extra string) string {
	return "test-key"
}

func TestDeriveCacheKeyAuthHeader(t *testing.T) {

	client := &PromTestClient{
		config: &config.OriginConfig{
			Paths: map[string]*config.PathConfig{
				"root": &config.PathConfig{
					Path:            "/",
					CacheKeyParams:  []string{"query", "step", "time"},
					CacheKeyHeaders: []string{headers.NameAuthorization},
				},
			},
		},
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1", nil)
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), client.Configuration(), nil, client.Configuration().Paths["root"]))
	tr.Header.Add("Authorization", "test")

	u := &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r := &model.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}, ClientRequest: tr}
	r.Headers = tr.Header

	key := DeriveCacheKey(r, nil, "extra")

	if key != "e2fc09c04a3281ff7d858f546068ec9e" {
		t.Errorf("expected %s got %s", "e2fc09c04a3281ff7d858f546068ec9e", key)
	}

}

func TestDeriveCacheKeyNoPathConfig(t *testing.T) {

	client := &PromTestClient{
		config: &config.OriginConfig{
			Paths: map[string]*config.PathConfig{
				"root": &config.PathConfig{
					Path:            "/",
					CacheKeyParams:  []string{"query", "step", "time"},
					CacheKeyHeaders: []string{},
				},
			},
		},
	}

	tr := httptest.NewRequest("GET", "http://127.0.0.1", nil)
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), client.Configuration(), nil, nil))

	u := &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r := &model.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}, ClientRequest: tr}
	key := DeriveCacheKey(r, nil, "extra")

	if key != "f53b04ce5c434a7357804ae15a64ee6c" {
		t.Errorf("expected %s got %s", "f53b04ce5c434a7357804ae15a64ee6c", key)
	}

}
