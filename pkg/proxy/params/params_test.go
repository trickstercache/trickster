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

package params

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestUpdateParams(t *testing.T) {
	params := url.Values{"param1": {"value1"}, "param3": {"value3"}, "param4": {"value4"}}
	updates := map[string]string{"param2": "value2", "+param3": "value3.1", "-param4": "", "": "empty_key_ignored"}
	expected := url.Values{"param1": {"value1"}, "param2": {"value2"}, "param3": {"value3", "value3.1"}}

	UpdateParams(params, nil)
	if len(params) != 3 {
		t.Errorf("expected %d got %d", 1, len(params))
	}

	UpdateParams(params, map[string]string{})
	if len(params) != 3 {
		t.Errorf("expected %d got %d", 1, len(params))
	}

	UpdateParams(params, updates)
	if !reflect.DeepEqual(params, expected) {
		t.Errorf("mismatch\nexpected: %v\n     got: %v\n", expected, params)
	}
}

func TestGetSetRequestValues(t *testing.T) {
	const params = "param1=value1"

	r, _ := http.NewRequest(http.MethodGet, "http://example.com/?"+params, nil)

	v, b, hb := GetRequestValues(r)
	if len(v) != 1 {
		t.Errorf("expected %d got %d", 1, len(v))
	}
	s := string(b)
	if s != params {
		t.Errorf("expected %s got %s", params, s)
	}
	if hb {
		t.Errorf("expected false")
	}

	v.Set("param2", "value2")
	SetRequestValues(r, v)
	v, b, hb = GetRequestValues(r)
	s = string(b)
	if len(v) != 2 {
		t.Errorf("expected %d got %d", 2, len(v))
	}
	if s == params || s == "" {
		t.Errorf("expected %s got %s", params+"&param2=value2", s)
	}
	if hb {
		t.Errorf("expected false")
	}

	r, _ = http.NewRequest(http.MethodPost, "http://example.com/", io.NopCloser(bytes.NewBufferString(params)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	v, b, hb = GetRequestValues(r)
	s = string(b)
	if len(v) != 1 {
		t.Errorf("expected %d got %d", 1, len(v))
	}
	if s != params {
		t.Errorf("expected %s got %s", params, s)
	}
	if !hb {
		t.Errorf("expected true")
	}

	v.Set("param2", "value2")
	SetRequestValues(r, v)
	v, b, hb = GetRequestValues(r)
	s = string(b)
	if len(v) != 2 {
		t.Errorf("expected %d got %d", 2, len(v))
	}
	if s == params || s == "" {
		t.Errorf("expected %s got %s", params+"&param2=value2", s)
	}
	if !hb {
		t.Errorf("expected true")
	}

	r, _ = http.NewRequest(http.MethodPost, "http://example.com/", io.NopCloser(bytes.NewBufferString(params)))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	v, b, hb = GetRequestValues(r)
	s = string(b)
	if len(v) != 0 {
		t.Errorf("expected %d got %d", 1, len(v))
	}
	if s != params {
		t.Errorf("expected %s got %s", params, s)
	}
	if !hb {
		t.Errorf("expected true")
	}
}

// TestGetRequestValues_POSTFormSplitParams verifies that when a client POSTs
// with some params in the URL query string and others in the urlencoded form
// body, GetRequestValues returns the MERGED set (previously it dropped the
// URL query entirely). This is the read-side companion to #969's fix to
// SetRequestValues.
func TestGetRequestValues_POSTFormSplitParams(t *testing.T) {
	// ?step=15 in URL, the rest in the form body.
	const body = "query=up&start=1000&end=2000"
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range?step=15",
		io.NopCloser(bytes.NewBufferString(body)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	v, _, hb := GetRequestValues(r)
	if !hb {
		t.Fatal("expected hasBody=true for form POST")
	}
	if got := v.Get("step"); got != "15" {
		t.Errorf("step param (URL) lost: got %q, want %q", got, "15")
	}
	if got := v.Get("query"); got != "up" {
		t.Errorf("query param (body): got %q, want %q", got, "up")
	}
	if got := v.Get("start"); got != "1000" {
		t.Errorf("start param (body): got %q, want %q", got, "1000")
	}
	if got := v.Get("end"); got != "2000" {
		t.Errorf("end param (body): got %q, want %q", got, "2000")
	}
	if len(v) != 4 {
		t.Errorf("expected 4 merged params, got %d: %v", len(v), v)
	}
}

// TestGetRequestValues_POSTFormBodyOverridesURL verifies that when the same
// key appears in both the URL query string and the form body, the body value
// replaces the URL value (single-valued merge, not append).
func TestGetRequestValues_POSTFormBodyOverridesURL(t *testing.T) {
	const body = "step=30"
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/?step=15",
		io.NopCloser(bytes.NewBufferString(body)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	v, _, _ := GetRequestValues(r)
	if got := v.Get("step"); got != "30" {
		t.Errorf("body should override URL on key conflict: got %q, want %q", got, "30")
	}
	if len(v["step"]) != 1 {
		t.Errorf("expected single merged value, got %v", v["step"])
	}
}

// TestSetRequestValues_SyncsRequestBodyCache verifies that SetRequestValues
// updates rsc.RequestBody so that CloneWithoutResources picks up the rewritten
// body rather than the stale pre-rewrite bytes. This is the guard against the
// TSM weighted-avg POST bug where sum/count sub-requests silently received the
// original avg query and produced a flat line of 1.
func TestSetRequestValues_SyncsRequestBodyCache(t *testing.T) {
	const origBody = "query=avg%28up%29&start=1000&end=2000&step=15"
	r, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	// Attach resources with the original body already cached, simulating the
	// state after the outer ServeHTTP has parsed the request.
	rsc := &request.Resources{RequestBody: []byte(origBody)}
	r = request.SetResources(r, rsc)

	// Rewrite query param (mirrors what RewriteForWeightedAvg does).
	v, _, _ := GetRequestValues(r)
	v.Set("query", "sum(up)")
	SetRequestValues(r, v)

	// rsc.RequestBody must reflect the rewritten params.
	got := request.GetResources(r)
	if got == nil {
		t.Fatal("expected non-nil resources after SetRequestValues")
	}
	gotVals, err := url.ParseQuery(string(got.RequestBody))
	if err != nil {
		t.Fatalf("RequestBody is not valid query-encoded: %v", err)
	}
	if q := gotVals.Get("query"); q != "sum(up)" {
		t.Errorf("RequestBody query param: got %q, want %q", q, "sum(up)")
	}
	if s := gotVals.Get("start"); s != "1000" {
		t.Errorf("RequestBody start param: got %q, want %q", s, "1000")
	}

	// A later GetRequestValues call must not reuse the stale PostForm parsed
	// before SetRequestValues rewrote the body.
	gotParsed, _, _ := GetRequestValues(r)
	if q := gotParsed.Get("query"); q != "sum(up)" {
		t.Errorf("reparsed query param: got %q, want %q", q, "sum(up)")
	}
}

func TestSetRequestValues_ClearsParsedFormCache(t *testing.T) {
	const origBody = "query=avg%28up%29&start=1000&end=2000&step=15"
	r, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	r = request.SetResources(r, &request.Resources{RequestBody: []byte(origBody)})

	v, _, _ := GetRequestValues(r)
	if q := v.Get("query"); q != "avg(up)" {
		t.Fatalf("initial query param: got %q, want %q", q, "avg(up)")
	}
	if r.PostForm == nil {
		t.Fatal("expected form cache to be populated")
	}

	v.Set("query", "count(up)")
	SetRequestValues(r, v)
	if r.Form != nil || r.PostForm != nil || r.MultipartForm != nil {
		t.Fatalf("expected parsed form caches cleared, got Form=%v PostForm=%v MultipartForm=%v",
			r.Form, r.PostForm, r.MultipartForm)
	}

	got, _, _ := GetRequestValues(r)
	if q := got.Get("query"); q != "count(up)" {
		t.Errorf("reparsed query param: got %q, want %q", q, "count(up)")
	}
}

// TestSetRequestValues_POSTJSONURLSync verifies that SetRequestValues for POST
// requests with JSON content-type also updates r.URL.RawQuery, so that
// subsequent reads via GetRequestValues (which reads from URL for POST+JSON)
// see the updated values. This is critical for cache key derivation after
// time-rounding in QueryHandler.
func TestSetRequestValues_POSTJSONURLSync(t *testing.T) {
	// Simulate the vmalert pattern: POST with JSON content-type,
	// params in URL query string, empty body.
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query?query=up&time=1001", nil)
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)

	// GetRequestValues should return URL query params for POST+JSON
	v, _, _ := GetRequestValues(r)
	if v.Get("query") != "up" {
		t.Fatalf("expected query=up, got %s", v.Get("query"))
	}
	if v.Get("time") != "1001" {
		t.Fatalf("expected time=1001, got %s", v.Get("time"))
	}

	// Simulate time-rounding: modify the time param
	v.Set("time", "1000")
	SetRequestValues(r, v)

	// After SetRequestValues, GetRequestValues must return the updated values.
	// This fails without the fix because SetRequestValues only wrote to body,
	// but GetRequestValues for POST+JSON reads from r.URL.Query().
	v2, _, _ := GetRequestValues(r)
	if v2.Get("time") != "1000" {
		t.Errorf("expected time=1000 after SetRequestValues, got %s", v2.Get("time"))
	}
	if v2.Get("query") != "up" {
		t.Errorf("expected query=up after SetRequestValues, got %s", v2.Get("query"))
	}
}
