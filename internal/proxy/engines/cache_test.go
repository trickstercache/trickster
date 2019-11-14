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
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/proxy/headers"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	ct "github.com/Comcast/trickster/internal/util/context"
)

func init() {
	log.Printf("Running on :3000 ...")
	go http.ListenAndServe("localhost:3000",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "", time.Now(),
				strings.NewReader("This is a test file, to see how the byte range requests work.\n"))
		}))
}

func TestCacheHitRangeRequest(t *testing.T) {
	expected := "is a "
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62")
	resp2.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, nil)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 5, End: 10}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}
	if (string(d2.Body[5:10])) != expected {
		t.Errorf("expected %s got %s", expected, string(d2.Body[5:10]))
	}
	if d2.UpdatedQueryRange != nil {
		t.Errorf("updated query range was expected to be empty")
	}
}

func TestPartialCacheMissRangeRequest(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "10")
	resp2.Header.Add("Content-Range", " bytes 0-10/62")
	resp2.StatusCode = 206
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a "), nil)

	b := model.Range{Start: 0, End: 10}
	r := make(model.Ranges, 1)
	r[0] = b
	d.Ranges = r

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, r)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 5, End: 20}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange[0].Start != 10 ||
		d2.UpdatedQueryRange[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestFullCacheMissRangeRequest(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "10")
	resp2.Header.Add("Content-Range", "bytes 0-10/62")
	resp2.StatusCode = 206
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a "), nil)

	b := model.Range{Start: 0, End: 10}
	r := make(model.Ranges, 1)
	r[0] = b
	d.Ranges = r

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, r)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 15, End: 20}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange[0].Start != 15 ||
		d2.UpdatedQueryRange[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestRangeRequestFromClient(t *testing.T) {
	client := &http.Client{}
	request, err := http.NewRequest("GET", "http://localhost:3000", nil)

	if err != nil {
		log.Fatalln(err)
	}
	request.Header.Set("Range", "bytes=10-25")
	resp, err := client.Do(request)

	bytes := make([]byte, resp.ContentLength)
	resp.Body.Read(bytes)
	fmt.Println(string(bytes))

	//--------------------------------------
	err = config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	d := model.DocumentFromHTTPResponse(resp, bytes, nil)
	r := model.GetByteRanges(request.Header.Get("Range"))
	err = WriteCache(cache, "testKey2", d, time.Duration(60)*time.Second, r)
	byteRange := model.Range{Start: 15, End: 20}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey2", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange != nil {
		t.Errorf("expected cache hit but got cache miss")
	}
	byteRange = model.Range{Start: 20, End: 35}
	ranges[0] = byteRange
	d2, err = QueryCache(cache, "testKey2", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange == nil {
		t.Errorf("expected cache miss but got cache hit")
	}
	if d2.UpdatedQueryRange[0].Start != 25 ||
		d2.UpdatedQueryRange[0].End != 35 {
		t.Errorf("expected start %d end %d, got start %d end %d", 25, 35, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestDeriveCacheKey(t *testing.T) {

	rpath := &config.PathConfig{
		Path:            "/",
		CacheKeyParams:  []string{"query", "step", "time"},
		CacheKeyHeaders: []string{},
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

func TestQueryCache(t *testing.T) {

	expected := "1234"

	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	resp := &http.Response{}
	resp.Header = make(http.Header)
	resp.StatusCode = 200
	resp.Header.Add("Content-Length", "4")
	d := model.DocumentFromHTTPResponse(resp, []byte(expected), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, nil)
	if err != nil {
		t.Error(err)
	}

	d2, err := QueryCache(cache, "testKey", nil)
	if err != nil {
		t.Error(err)
	}

	if string(d2.Body) != string(expected) {
		t.Errorf("expected %s got %s", string(expected), string(d2.Body))
	}

	if d2.StatusCode != 200 {
		t.Errorf("expected %d got %d", 200, d2.StatusCode)
	}

	_, err = QueryCache(cache, "testKey2", nil)
	if err == nil {
		t.Errorf("expected error")
	}

}

func TestGetResponseCachingPolicy(t *testing.T) {

	now := time.Now().Truncate(time.Second)

	tests := []struct {
		a           http.Header
		expectedTTL time.Duration
	}{
		{ // 0 - Cache-Control: no-store
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoStore},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 1 -  Cache-Control: no-cache
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoCache},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 2 - Cache-Control: max-age=300
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge + "=300"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 3 - Cache-Control: max-age=   should come back as -1 ttl
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge + "="},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 4 - Cache-Control: max-age (no =anything)  should come back as 0 ttl
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 5 - Cache-Control: private,max-age=300  should be treated as non-cacheable by proxy
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePrivate + "," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 6 - Cache-Control: public,max-age=300  should be treated as cacheable by proxy
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 7 - Cache-Control and Expires, Cache-Control should win
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
				headers.NameExpires:      []string{"-1"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 8 - Cache-Control and LastModified, Cache-Control should win
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 9 - Already Expired (could not parse)
			a: http.Header{
				headers.NameExpires: []string{"-1"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 10 - Already Expired (parseable in the past)
			a: http.Header{
				headers.NameExpires: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 11 - Expires in an hour
			a: http.Header{
				headers.NameDate:    []string{now.Format(time.RFC1123)},
				headers.NameExpires: []string{now.Add(time.Hour * time.Duration(1)).Format(time.RFC1123)},
			},
			expectedTTL: 1 * time.Hour,
		},
		{ // 12 - Synthesized TTL from Last Modified
			a: http.Header{
				headers.NameDate:         []string{now.Format(time.RFC1123)},
				headers.NameLastModified: []string{now.Add(-time.Hour * time.Duration(5)).Format(time.RFC1123)},
			},
			expectedTTL: 1 * time.Hour,
		},
		{ // 13 - No Cache Control Response Headers
			a: http.Header{
				headers.NameDate: []string{now.Format(time.RFC1123)},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 14 - Invalid Date Header Format
			a: http.Header{
				headers.NameDate:    []string{"1571338193"},
				headers.NameExpires: []string{"-1"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 15 - Invalid Date Header Format
			a: http.Header{
				headers.NameETag: []string{"etag-test"},
			},
			expectedTTL: 0,
		},
		{ // 16 - Invalid Last Modified Date Header Format
			a: http.Header{
				headers.NameLastModified: []string{"1571338193"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 17 - Must Revalidate
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMustRevalidate},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: 0,
		},
		{ // 18 - NoTransform
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoTransform},
			},
			expectedTTL: -1 * time.Second,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			p := GetResponseCachingPolicy(200, nil, test.a)
			d := time.Duration(p.FreshnessLifetime) * time.Second
			if test.expectedTTL != d {
				t.Errorf("expected ttl of %d got %d", test.expectedTTL, d)
			}
		})
	}
}

func TestGetResponseCachingPolicyNegativeCache(t *testing.T) {
	p := GetResponseCachingPolicy(400, map[int]time.Duration{400: 300 * time.Second}, nil)
	if p.FreshnessLifetime != 300 {
		t.Errorf("expected ttl of %d got %d", 300, p.FreshnessLifetime)
	}
}

func TestGetRequestCacheability(t *testing.T) {

	tests := []struct {
		a           http.Header
		isCacheable bool
	}{
		{ // 0 - Cache-Control: no-store
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoStore},
			},
			isCacheable: false,
		},
		{ // 1 -  Cache-Control: no-cache
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoCache},
			},
			isCacheable: false,
		},
		{ // 2 - No Cache Control Request Headers
			a:           http.Header{},
			isCacheable: true,
		},
		{ // 3 - Pragma: NoCache
			a: http.Header{
				headers.NamePragma: []string{headers.ValueNoCache},
			},
			isCacheable: false,
		},
		{ // 4 - IMS
			a: http.Header{
				headers.NameIfModifiedSince: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			isCacheable: true,
		},
		{ // 5 - IUS
			a: http.Header{
				headers.NameIfUnmodifiedSince: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			isCacheable: true,
		},
		{ // 6 - INM
			a: http.Header{
				headers.NameIfNoneMatch: []string{"test-string"},
			},
			isCacheable: true,
		},
		{ // 7 - IM
			a: http.Header{
				headers.NameIfMatch: []string{"test-string"},
			},
			isCacheable: true,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			p := GetRequestCachingPolicy(test.a)
			ic := !p.NoCache
			if ic != test.isCacheable {
				t.Errorf("mismatch isCacheable expected %v got %v", test.isCacheable, ic)
			}
		})
	}

}
