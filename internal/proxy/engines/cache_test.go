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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/proxy/headers"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	ct "github.com/Comcast/trickster/internal/util/context"
)

func TestDeriveCacheKey(t *testing.T) {

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
	tr = tr.WithContext(ct.WithConfigs(tr.Context(), client.Configuration(), nil, client.Configuration().Paths["root"]))

	u := &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r := &model.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}, ClientRequest: tr}
	key := DeriveCacheKey(client, r, "extra")

	if key != "6667a75e76dea9a5cd6c6ba73e5825b5" {
		t.Errorf("expected %s got %s", "6667a75e76dea9a5cd6c6ba73e5825b5", key)
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
	d := model.DocumentFromHTTPResponse(resp, []byte(expected), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	d2, err := QueryCache(cache, "testKey")
	if err != nil {
		t.Error(err)
	}

	if string(d2.Body) != string(expected) {
		t.Errorf("expected %s got %s", string(expected), string(d2.Body))
	}

	if d2.StatusCode != 200 {
		t.Errorf("expected %d got %d", 200, d2.StatusCode)
	}

	_, err = QueryCache(cache, "testKey2")
	if err == nil {
		t.Errorf("expected error")
	}

}

// example headers:
// date: Sun, 16 Jun 2019 14:19:04 GMT
// expires: -1
// cache-control: private, max-age=0
// content-type: text/html; charset=ISO-8859-1
// p3p: CP="This is not a P3P policy! See g.co/p3phelp for more info."
// server: gws
// x-xss-protection: 0
// x-frame-options: SAMEORIGIN
// set-cookie: 1P_JAR=2019-06-16-14; expires=Tue, 16-Jul-2019 14:19:04 GMT; path=/; domain=.google.com
// set-cookie: NID=185=RXv4GLcLUhGFKcGW1Yo6cKvRKddyqh9xO4Ehex3VCcRz5karTWvsfwUbjKUJR-ENolG76IjNX07dY7RFr41cH5wpNOUadbUyQ9TX8jNmTI2C5NAyl_ORwrvwhmhvNFF3u_CrSaYi4mOqOqt6Q1brO0whSpzwxOIYvbQ8F8Q4vEs; expires=Mon, 16-Dec-2019 14:19:04 GMT; path=/; domain=.google.com; HttpOnly
// alt-svc: quic=":443"; ma=2592000; v="46,44,43,39"
// accept-ranges: none
// vary: Accept-Encoding

func TestGetResponseCacheability(t *testing.T) {

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
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			p := GetResponseCachingPolicy(200, nil, test.a, 0)
			ttl := time.Duration(p.FreshnessLifetime) * time.Second
			if ttl != test.expectedTTL {
				t.Errorf("mismatch ttl expected %v got %v", test.expectedTTL, ttl)
			}
		})
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
