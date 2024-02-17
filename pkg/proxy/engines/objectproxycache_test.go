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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/mockster/pkg/mocks/byterange"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func setupTestHarnessOPC(file, body string, code int,
	headers map[string]string) (*httptest.Server, *httptest.ResponseRecorder,
	*http.Request, *request.Resources, error) {
	return setupTestHarnessOPCByType(file, "test", "/opc", body, code, headers)
}

func setupTestHarnessOPCRange(hdr map[string]string) (*httptest.Server,
	*httptest.ResponseRecorder, *http.Request, *request.Resources, error) {
	s, rr, r, rsc, err := setupTestHarnessOPCByType("", "rangesim", "/byterange/opc", "", 0, hdr)
	return s, rr, r, rsc, err
}

func setupTestHarnessOPCByType(
	file, serverType, path, body string, code int, headers map[string]string,
) (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *request.Resources, error) {

	backendClient, err := NewTestClient("test", nil, nil, nil, nil)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}
	ts, w, r, _, err := tu.NewTestInstance(file, backendClient.DefaultPathConfigs,
		code, body, headers, serverType, path, "debug")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}
	r.RequestURI = ""
	rsc := request.GetResources(r)
	backendClient, err = NewTestClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}
	client := backendClient.(*TestClient)
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()
	rsc.BackendClient = client
	rsc.Tracer = tu.NewTestTracer()
	pc := rsc.PathConfig

	if pc == nil {
		return nil, nil, nil, nil, fmt.Errorf("could not find path %s", "/")
	}

	pc.CacheKeyParams = []string{"rangeKey", "instantKey"}

	return ts, w, r, rsc, nil
}

func setupTestHarnessOPCWithPCF(file, body string, code int, headers map[string]string) (*httptest.Server,
	*httptest.ResponseRecorder, *http.Request, *request.Resources, error) {

	backendClient, err := NewTestClient("test", nil, nil, nil, nil)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}
	ts, w, r, _, err := tu.NewTestInstance(file, backendClient.DefaultPathConfigs, code, body, headers,
		"prometheus", "/prometheus/api/v1/query", "debug")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}
	rsc := request.GetResources(r)
	backendClient, err = NewTestClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}
	client := backendClient.(*TestClient)
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()
	rsc.BackendClient = client
	rsc.Tracer = tu.NewTestTracer()

	pc := rsc.PathConfig

	if pc == nil {
		return nil, nil, nil, nil, fmt.Errorf("could not find path %s", "/prometheus/api/v1/query")
	}

	pc.CollapsedForwardingName = "progressive"
	pc.CollapsedForwardingType = forwarding.CFTypeProgressive

	pc.CacheKeyParams = []string{"rangeKey", "instantKey"}

	return ts, w, r, rsc, nil
}

func TestObjectProxyCacheRequest(t *testing.T) {

	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusPartialContent, hdrs)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	r.Header.Add(headers.NameRange, "bytes=0-3")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	_, e := testFetchOPC(r, http.StatusPartialContent, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	// get cache hit coverage too by repeating:
	_, e = testFetchOPC(r, http.StatusPartialContent, "test", map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	// Remove Cache Hit from the Response Handler Map to test unknown handler error condition
	delete(cacheResponseHandlers, status.LookupStatusHit)

	_, e = testFetchOPC(r, http.StatusPartialContent, "test", map[string]string{"status": "proxy-only"})
	for _, err = range e {
		t.Error(err)
	}

	// add cache hit back
	cacheResponseHandlers[status.LookupStatusHit] = handleCacheKeyHit

}

func TestObjectProxyCachePartialHit(t *testing.T) {

	ts, _, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// Cache miss on range
	r.Header.Set(headers.NameRange, "bytes=0-10")
	expectedBody, err := getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}

	_, e := testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	// Partial Hit on an overlapping range
	r.Header.Set(headers.NameRange, "bytes=5-15")

	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	// Range Miss on an separate range
	r.Header.Set(headers.NameRange, "bytes=60-70")

	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "rmiss"})
	for _, err = range e {
		t.Error(err)
	}

	// Partial Hit on an multiple ranges
	r.Header.Set(headers.NameRange, "bytes=10-20,50-55,60-65,69-75")
	expectedBody, err = getExpectedRangeBody(r, "d5a5acd7eb4d3f622c62947a9904b89b")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	// Fulfill the cache with the remaining parts
	r.Header.Del(headers.NameRange)
	_, e = testFetchOPC(r, http.StatusOK, byterange.Body, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	// Test Articulated Upstream
	rsc.BackendOptions.DearticulateUpstreamRanges = true
	r.Header.Set(headers.NameRange, "bytes=10-20,50-55,60-65,69-75")
	r.URL.Path = "/byterange/new/test/path"
	expectedBody, err = getExpectedRangeBody(r, "d5a5acd7eb4d3f622c62947a9904b89b")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestFullArticuation(t *testing.T) {

	ts, _, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	// Test Articulated Upstream
	rsc.BackendOptions.DearticulateUpstreamRanges = true
	rsc.BackendOptions.RevalidationFactor = 2
	r.Header.Set(headers.NameRange, "bytes=10-20,50-55,60-65,69-75")
	r.URL.Path = "/byterange/new/test/path"
	r.URL.RawQuery = "max-age=1"
	expectedBody, err := getExpectedRangeBody(r, "d5a5acd7eb4d3f622c62947a9904b89b")
	if err != nil {
		t.Error(err)
	}
	_, e := testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.RawQuery = "max-age=1&status=200"
	r.URL.Path = "/byterange/new/test/path/2"
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.RawQuery = "max-age=1&ims=200"
	r.URL.Path = "/byterange/new/test/path/3"
	r.Header.Set(headers.NameRange, "bytes=10-20")
	expectedBody, err = getExpectedRangeBody(r, "d5a5acd7eb4d3f622c62947a9904b89b")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=10-20, 25-30, 45-60")
	expectedBody, err = getExpectedRangeBody(r, "a262725e1b8ae4967d369cff746e3924")
	if err != nil {
		t.Error(err)
	}
	r.URL.RawQuery = "max-age=1&ims=206"
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=9-20, 25-31, 42-65, 70-80")
	expectedBody, err = getExpectedRangeBody(r, "34b73ea5c4c1ab5b9e34c9888119c58f")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 3000)

	r.Header.Set(headers.NameRange, "bytes=9-20, 90-95, 100-105")
	expectedBody, err = getExpectedRangeBody(r, "01760208a2d6589fc9620627d561640d")
	if err != nil {
		t.Error(err)
	}
	r.URL.RawQuery = "max-age=1&ims=206"
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=9-20, 90-95, 100-105")
	expectedBody, err = getExpectedRangeBody(r, "01760208a2d6589fc9620627d561640d")
	if err != nil {
		t.Error(err)
	}
	r.URL.Path = "/byterange/new/test/path/20"
	r.URL.RawQuery = "max-age=1"
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=9-20, 25-32, 41-65")
	expectedBody, err = getExpectedRangeBody(r, "722af19813169c99d8bda37a2f244f39")
	if err != nil {
		t.Error(err)
	}
	r.URL.RawQuery = "max-age=1&ims=206&non-ims=206"
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 1050)

	r.Header.Del(headers.NameRange)
	r.URL.RawQuery = "max-age=1"
	_, e = testFetchOPC(r, http.StatusOK, byterange.Body, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=9-20, 21-22")
	r.URL.Path = "/byterange/new/test/path/21"
	expectedBody, err = getExpectedRangeBody(r, "368b9fbcef800068a48e70fa6e040289")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=0-1223")
	r.URL.Path = "/byterange/new/test/path/22"
	r.URL.RawQuery = ""
	expectedBody, err = getExpectedRangeBody(r, "722af19813169c99d8bda37a2f244f39")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=0-1220,1221-1223")
	expectedBody, err = getExpectedRangeBody(r, "f8813b96e6b06ea1d826bb921690f87b")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Del(headers.NameRange)
	_, e = testFetchOPC(r, http.StatusOK, byterange.Body, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

}

func TestObjectProxyCachePartialHitNotFresh(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: rsc.BackendOptions})

	pr := newProxyRequest(r, w)
	o := rsc.BackendOptions
	cc := rsc.CacheClient
	pr.cachingPolicy = GetRequestCachingPolicy(pr.Header)
	pr.key = o.Host + "." + pr.DeriveCacheKey("")
	pr.cacheDocument, pr.cacheStatus, pr.neededRanges, _ = QueryCache(ctx, cc, pr.key, pr.wantedRanges, nil)
	handleCacheKeyMiss(pr)

	pr.cachingPolicy.CanRevalidate = false
	pr.cachingPolicy.IsFresh = false
	pr.cachingPolicy.FreshnessLifetime = 0

	pr.store()

	handleCachePartialHit(pr)

	if pr.isPartialResponse {
		t.Errorf("Expected full response, got %t", pr.isPartialResponse)
	}

	if pr.cacheStatus != status.LookupStatusKeyMiss {
		t.Errorf("Expected %s, got %s", status.LookupStatusKeyMiss, pr.cacheStatus)
	}
}

func TestObjectProxyCachePartialHitFullResponse(t *testing.T) {

	ts, w, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: rsc.BackendOptions})

	pr := newProxyRequest(r, w)
	o := rsc.BackendOptions
	cc := rsc.CacheClient
	pr.cachingPolicy = GetRequestCachingPolicy(pr.Header)
	pr.key = o.Host + "." + pr.DeriveCacheKey("")
	pr.cacheDocument, pr.cacheStatus, pr.neededRanges, _ = QueryCache(ctx, cc, pr.key, pr.wantedRanges, nil)
	handleCacheKeyMiss(pr)
	handleCachePartialHit(pr)

	if pr.isPartialResponse {
		t.Errorf("Expected full response, got %t", pr.isPartialResponse)
	}
}

func TestObjectProxyCacheRangeMiss(t *testing.T) {

	ts, _, r, _, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	r.Header.Set(headers.NameRange, "bytes=0-10")
	expectedBody, err := getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e := testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=15-20")
	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "rmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheRevalidation(t *testing.T) {

	ts, _, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	rsc.BackendOptions.RevalidationFactor = 2

	r.Header.Set(headers.NameRange, "bytes=0-10")
	if rsc.PathConfig == nil {
		t.Error("nil path config")
	}

	if rsc.PathConfig.ResponseHeaders == nil {
		rsc.PathConfig.ResponseHeaders = make(map[string]string)
	}
	rsc.PathConfig.ResponseHeaders[headers.NameCacheControl] = "max-age=1"

	expectedBody, err := getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e := testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 1010)

	r.Header.Set(headers.NameRange, "bytes=0-10")
	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "rhit"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 1010)
	r.Header.Set(headers.NameRange, "bytes=0-15")
	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	// purge the cache
	r.Header.Del(headers.NameRange)
	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusOK, expectedBody, map[string]string{"status": "proxy-only"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/10"

	r.Header.Del(headers.NameCacheControl)

	// now store it with an earlier last modified header
	r.Header.Del(headers.NameCacheControl)
	rsc.PathConfig.ResponseHeaders[headers.NameLastModified] = time.Unix(1577836799, 0).UTC().Format(time.RFC1123)
	rsc.PathConfig.ResponseHeaders["-"+headers.NameCacheControl] = ""
	rsc.PathConfig.ResponseHeaders[headers.NameExpires] = time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC1123)

	expectedBody, err = getExpectedRangeBody(r, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusOK, expectedBody, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheRequestWithPCF(t *testing.T) {

	headers := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPCWithPCF("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	r.Header.Set("testHeaderName", "testHeaderValue")

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

}

func TestObjectProxyCacheTrueHitNoDocumentErr(t *testing.T) {

	pr := &proxyRequest{}
	err := handleTrueCacheHit(pr)
	if err != errors.ErrNilCacheDocument {
		t.Errorf("expected %s got %s", errors.ErrNilCacheDocument, err)
	}
}

func TestObjectProxyCacheRequestClientNoCache(t *testing.T) {

	ts, _, r, _, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "proxy-only"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestFetchViaObjectProxyCacheRequestClientNoCache(t *testing.T) {

	ts, _, r, _, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	r.Header.Set(headers.NameCacheControl, headers.ValueNoCache)

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "proxy-only"})
	for _, err = range e {
		t.Error(err)
	}

	_, _, b := FetchViaObjectProxyCache(r)
	if b {
		t.Errorf("expected %t got %t", false, b)
	}
}

func TestObjectProxyCacheRequestOriginNoCache(t *testing.T) {

	headers := map[string]string{"Cache-Control": "no-cache"}
	ts, _, r, _, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheIMS(t *testing.T) {

	hdrs := map[string]string{"Cache-Control": "max-age=1"}
	ts, _, r, rsc, err := setupTestHarnessOPCRange(hdrs)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	rsc.BackendOptions.RevalidationFactor = 2

	_, e := testFetchOPC(r, http.StatusOK, byterange.Body, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameIfModifiedSince, "Wed, 01 Jan 2020 00:00:00 UTC")

	_, e = testFetchOPC(r, http.StatusNotModified, "", map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 1050)

	r.URL.RawQuery = "status=200"

	_, e = testFetchOPC(r, http.StatusNotModified, "", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheINM(t *testing.T) {

	rh := map[string]string{headers.NameCacheControl: "max-age=60", headers.NameETag: "test"}
	ts, _, r, _, err := setupTestHarnessOPC("", "test", http.StatusOK, rh)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameIfNoneMatch, `"test"`)
	_, e = testFetchOPC(r, http.StatusNotModified, "", map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameIfNoneMatch, `W/"test2"`)
	_, e = testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheNoRevalidate(t *testing.T) {

	headers := map[string]string{headers.NameCacheControl: headers.ValueMaxAge + "=1"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, headers)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := rsc.PathConfig
	p.ResponseHeaders = headers

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(1010 * time.Millisecond)

	_, e = testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheCanRevalidate(t *testing.T) {

	headers := map[string]string{
		headers.NameCacheControl: headers.ValueMaxAge + "=1",
		headers.NameETag:         "test-etag",
	}
	ts, _, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	p := rsc.PathConfig
	p.ResponseHeaders = headers
	rsc.BackendOptions.RevalidationFactor = 2

	_, e := testFetchOPC(r, http.StatusOK, byterange.Body, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	time.Sleep(1010 * time.Millisecond)

	_, e = testFetchOPC(r, http.StatusOK, byterange.Body, map[string]string{"status": "rhit"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheRevalidated(t *testing.T) {

	const dt = "Sun, 16 Jun 2019 14:19:04 GMT"

	hdr := map[string]string{
		headers.NameCacheControl: headers.ValueMaxAge + "=2",
		headers.NameLastModified: dt,
	}
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, hdr)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	rsc.PathConfig.ResponseHeaders = hdr

	_, e := testFetchOPC(r, http.StatusOK, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameIfModifiedSince, dt)
	_, e = testFetchOPC(r, http.StatusNotModified, "", map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestObjectProxyCacheRequestNegativeCache(t *testing.T) {

	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusNotFound, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	pc := po.New()
	cfg := rsc.BackendOptions
	cfg.Paths = map[string]*po.Options{
		"/": pc,
	}
	r = r.WithContext(tc.WithResources(r.Context(), request.NewResources(cfg, pc, rsc.CacheConfig,
		rsc.CacheClient, rsc.BackendClient, nil, rsc.Logger)))

	// Remove negative cache for first request
	delete(cfg.NegativeCache, 404)

	_, e := testFetchOPC(r, http.StatusNotFound, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	// request again, should still cache miss, but this time, Negative Cache 404's for 30s
	cfg.NegativeCache[404] = time.Second * 30

	_, e = testFetchOPC(r, http.StatusNotFound, "test", map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	// request again, this time it should be a cache hit.
	_, e = testFetchOPC(r, http.StatusNotFound, "test", map[string]string{"status": "nchit"})
	for _, err = range e {
		t.Error(err)
	}
}

func TestHandleCacheRevalidation(t *testing.T) {

	ts, _, r, _, err := setupTestHarnessOPC("", "test", http.StatusNotFound, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	pr := newProxyRequest(r, nil)
	pr.cacheStatus = status.LookupStatusRangeMiss
	pr.cachingPolicy = &CachingPolicy{}

	err = handleCacheRevalidation(pr)
	if err != nil {
		t.Error(err)
	}
}

func getExpectedRangeBody(r *http.Request, boundary string) (string, error) {

	client := &http.Client{}
	resp, err := client.Do(r)
	if err != nil {
		return "", err
	}
	b, _ := io.ReadAll(resp.Body)
	expectedBody := string(b)

	if boundary != "" {
		expectedBody = strings.Replace(expectedBody, "TestRangeServerBoundary", boundary, -1)
	}

	return expectedBody, nil
}

func TestRangesExhaustive(t *testing.T) {

	ts, _, r, rsc, err := setupTestHarnessOPCRange(nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	rsc.BackendOptions.RevalidationFactor = 2
	rsc.BackendOptions.DearticulateUpstreamRanges = true

	r.URL.Path = "/byterange/test/1"
	r.Header.Set(headers.NameRange, "bytes=0-6,25-32")
	req := r.Clone(context.Background())
	expectedBodyA, err := getExpectedRangeBody(req, "563a7014513fc6f0cbb4e8632dd107fc")
	if err != nil {
		t.Error(err)
	}
	_, e := testFetchOPC(r, http.StatusPartialContent, expectedBodyA, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=0-10,20-28")
	req = r.Clone(context.Background())
	expectedBody, err := getExpectedRangeBody(req, "33f2477458123b02034bfbe20c52d949")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=0-6")
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "33f2477458123b02034bfbe20c52d949")
	if err != nil {
		t.Error(err)
	}

	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=5-7")
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "33f2477458123b02034bfbe20c52d949")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=29-29")
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "33f2477458123b02034bfbe20c52d949")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=9-22,28-60")
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "1fd80b6b357b4608027dd500ad3f3c21")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Del(headers.NameRange)
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "1fd80b6b357b4608027dd500ad3f3c21")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusOK, expectedBody, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=0-10,20-28")
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "33f2477458123b02034bfbe20c52d949")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	r.Header.Set(headers.NameRange, "bytes=0-6")
	req = r.Clone(context.Background())
	expectedBody, err = getExpectedRangeBody(req, "33f2477458123b02034bfbe20c52d949")
	if err != nil {
		t.Error(err)
	}

	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody, map[string]string{"status": "hit"})
	for _, err = range e {
		t.Error(err)
	}

	// Test Range Revalidiations

	rsc.PathConfig.ResponseHeaders = map[string]string{headers.NameCacheControl: "max-age=1"}

	r.URL.Path = "/byterange/test/2"
	r.Header.Set(headers.NameRange, "bytes=0-6")
	req = r.Clone(context.Background())
	expectedBody1, err := getExpectedRangeBody(req, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody1, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/3"
	r.Header.Set(headers.NameRange, "bytes=0-6, 8-10")
	req = r.Clone(context.Background())
	expectedBody2, err := getExpectedRangeBody(req, "1b4e59d25d723e317359c5e542d80f5c")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody2, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/4"
	r.Header.Set(headers.NameRange, "bytes=0-6, 8-10")
	req = r.Clone(context.Background())
	expectedBody3, err := getExpectedRangeBody(req, "1b4e59d25d723e317359c5e542d80f5c")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody3, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/5"
	r.Header.Set(headers.NameRange, "bytes=6-20")
	req = r.Clone(context.Background())
	expectedBody4, err := getExpectedRangeBody(req, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody4, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/6"
	r.Header.Set(headers.NameRange, "bytes=6-20")
	req = r.Clone(context.Background())
	expectedBody5, err := getExpectedRangeBody(req, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody5, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/7"
	r.Header.Set(headers.NameRange, "bytes=6-20")
	req = r.Clone(context.Background())
	expectedBody6, err := getExpectedRangeBody(req, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody6, map[string]string{"status": "kmiss"})
	for _, err = range e {
		t.Error(err)
	}

	// Now sleep to let them expire but not purge
	time.Sleep(1050 * time.Millisecond)

	// Now make more requests that require a revalidation first.

	r.URL.Path = "/byterange/test/2"
	r.Header.Set(headers.NameRange, "bytes=0-6")
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody1, map[string]string{"status": "rhit"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/3"
	r.Header.Set(headers.NameRange, "bytes=0-6, 8-10")
	expectedBody2 = strings.Replace(expectedBody2, "TestRangeServerBoundary", "1b4e59d25d723e317359c5e542d80f5c", -1)
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody2, map[string]string{"status": "rhit"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/4"
	r.Header.Set(headers.NameRange, "bytes=5-9")
	req = r.Clone(context.Background())
	expectedBody3, err = getExpectedRangeBody(req, "1b4e59d25d723e317359c5e542d80f5c")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody3, map[string]string{"status": "phit"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/5"
	r.Header.Set(headers.NameRange, "bytes=0-5")
	req = r.Clone(context.Background())
	expectedBody4, err = getExpectedRangeBody(req, "")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody4, map[string]string{"status": "rmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/6"
	r.Header.Set(headers.NameRange, "bytes=0-5,21-30")
	req = r.Clone(context.Background())
	expectedBody5, err = getExpectedRangeBody(req, "d51d39834c9650e17cc486c4a52cf572")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody5, map[string]string{"status": "rmiss"})
	for _, err = range e {
		t.Error(err)
	}

	r.URL.Path = "/byterange/test/7"
	r.Header.Set(headers.NameRange, "bytes=22-30,32-40")
	req = r.Clone(context.Background())
	expectedBody6, err = getExpectedRangeBody(req, "bab29463882afe6d6033e88dc74d2bdd")
	if err != nil {
		t.Error(err)
	}
	_, e = testFetchOPC(r, http.StatusPartialContent, expectedBody6, map[string]string{"status": "rmiss"})
	for _, err = range e {
		t.Error(err)
	}
}

func testFetchOPC(r *http.Request, sc int, body string,
	match map[string]string) (*httptest.ResponseRecorder, []error) {

	e := make([]error, 0)

	w := httptest.NewRecorder()

	ObjectProxyCacheRequest(w, r)
	resp := w.Result()

	err := testStatusCodeMatch(resp.StatusCode, sc)
	if err != nil {
		e = append(e, err)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		e = append(e, err)
	}

	err = testStringMatch(string(bodyBytes), body)
	if err != nil {
		e = append(e, err)
	}

	err = testResultHeaderPartMatch(resp.Header, match)
	if err != nil {
		e = append(e, err)
	}

	if len(e) == 0 {
		e = nil
	}

	return w, e

}

func TestFetchViaObjectProxyCacheRequestErroringCache(t *testing.T) {

	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Error(err)
	}
	defer ts.Close()

	tc := &testCache{configuration: rsc.CacheConfig, locker: locks.NewNamedLocker()}
	rsc.CacheClient = tc
	tc.configuration.Provider = "test"

	_, _, b := FetchViaObjectProxyCache(r)
	if b {
		t.Errorf("expected %t got %t", false, b)
	}
}

func TestRerunRequest(t *testing.T) {
	ts, _, r, _, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	w := httptest.NewRecorder()
	pr := newProxyRequest(r, w)
	locker := locks.NewNamedLocker()
	nl, _ := locker.Acquire("test")
	pr.cacheLock = nl
	pr.hasWriteLock = true
	rerunRequest(pr)
	if !pr.wasReran {
		t.Error("expected true")
	}
}
