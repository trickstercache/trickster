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

package sql

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const identityQuery = "SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(v) " +
	"FROM m WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1"

func jsonPost(t *testing.T, body string) *http.Request {
	t.Helper()
	b := []byte(body)
	return &http.Request{
		Method:        http.MethodPost,
		URL:           &url.URL{Path: "/api/v3/query_sql"},
		Header:        http.Header{headers.NameContentType: {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(b)),
		ContentLength: int64(len(b)),
	}
}

// TestJSONBodyDBInCacheIdentity ensures requests differing only by the
// JSON-body db field never share a cache entry.
func TestJSONBodyDBInCacheIdentity(t *testing.T) {
	r := jsonPost(t, `{"db":"prod","q":"`+identityQuery+`"}`)
	trq, _, _, err := ParseTimeRangeQuery(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	if trq.CacheKeyElements[ParamDB] != "prod" {
		t.Fatalf("db missing from cache identity: %v", trq.CacheKeyElements)
	}
}

// TestParameterizedQueryBypassesDelta ensures a params-bearing request is
// routed to the object cache with its parameters in the cache identity and
// its body untouched.
func TestParameterizedQueryBypassesDelta(t *testing.T) {
	body := `{"db":"prod","q":"SELECT * FROM m WHERE host = $host","params":{"host":"a"}}`
	r := jsonPost(t, body)
	trq, ro, canOPC, err := ParseTimeRangeQuery(r, iofmt.Detect(r))
	if !errors.Is(err, ErrParameterizedQuery) || !canOPC || trq == nil {
		t.Fatalf("ParseTimeRangeQuery = %v/%t/%v", trq, canOPC, err)
	}
	if trq.CacheKeyElements[ParamParams] != `host="a";` {
		t.Fatalf("params missing from cache identity: %v", trq.CacheKeyElements)
	}
	if trq.CacheKeyElements[ParamDB] != "prod" {
		t.Fatalf("db missing from cache identity: %v", trq.CacheKeyElements)
	}
	if string(trq.OriginalBody) != body {
		t.Fatalf("original body not preserved: %s", trq.OriginalBody)
	}
	if ro == nil {
		t.Fatal("nil request options")
	}
	// the request body must remain untouched for pass-through
	b, _ := io.ReadAll(r.Body)
	if string(b) != body {
		t.Fatalf("request body was rewritten: %s", b)
	}
}

// TestEncodeBodyPreservesFields ensures upstream body rewrites keep db and
// other request-document fields intact.
func TestEncodeBodyPreservesFields(t *testing.T) {
	r := jsonPost(t, `{"db":"prod","q":"old"}`)
	out := string(EncodeBody(r, "new statement"))
	if !strings.Contains(out, `"db":"prod"`) || !strings.Contains(out, `"q":"new statement"`) {
		t.Fatalf("EncodeBody dropped fields: %s", out)
	}

	form := []byte(url.Values{"q": {"old"}, "db": {"prod"}}.Encode())
	fr := &http.Request{
		Method:        http.MethodPost,
		URL:           &url.URL{Path: "/api/v3/query_sql"},
		Header:        http.Header{headers.NameContentType: {"application/x-www-form-urlencoded"}},
		Body:          io.NopCloser(bytes.NewReader(form)),
		ContentLength: int64(len(form)),
	}
	vals, err := url.ParseQuery(string(EncodeBody(fr, "new statement")))
	if err != nil || vals.Get("db") != "prod" || vals.Get("q") != "new statement" {
		t.Fatalf("form EncodeBody dropped fields: %v %v", vals, err)
	}
}

// TestBodyFormatHonored ensures a JSON-body format field selects the output
// format when the URL has none.
func TestBodyFormatHonored(t *testing.T) {
	r := jsonPost(t, `{"q":"`+identityQuery+`","format":"csv"}`)
	_, ro, _, err := ParseTimeRangeQuery(r, iofmt.Detect(r))
	if err != nil {
		t.Fatal(err)
	}
	if ro.OutputFormat != iofmt.V3OutputCSV {
		t.Fatalf("body format ignored: %d", ro.OutputFormat)
	}
}

// TestUnsupportedFormatDetection covers the proxy-through gate for formats
// Trickster cannot reserialize.
func TestUnsupportedFormatDetection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		request   *http.Request
		supported bool
	}{
		{"url parquet", &http.Request{Method: http.MethodGet, URL: &url.URL{
			Path: "/api/v3/query_sql", RawQuery: "q=SELECT+1&format=parquet"}}, false},
		{"url pretty", &http.Request{Method: http.MethodGet, URL: &url.URL{
			Path: "/api/v3/query_sql", RawQuery: "q=SELECT+1&format=pretty"}}, false},
		{"url csv", &http.Request{Method: http.MethodGet, URL: &url.URL{
			Path: "/api/v3/query_sql", RawQuery: "q=SELECT+1&format=csv"}}, true},
		{"none", &http.Request{Method: http.MethodGet, URL: &url.URL{
			Path: "/api/v3/query_sql", RawQuery: "q=SELECT+1"}}, true},
		{"body parquet", jsonPost(t, `{"q":"SELECT 1","format":"parquet"}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupportedV3Format(tc.request); got != tc.supported {
				t.Fatalf("SupportedV3Format = %t, want %t", got, tc.supported)
			}
		})
	}
}
