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
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestCheckCacheFreshness(t *testing.T) {
	tests := []struct {
		name      string
		policy    *CachingPolicy
		wantFresh bool
	}{
		{
			name:      "nil policy",
			policy:    nil,
			wantFresh: false,
		},
		{
			name: "fresh — lifetime not expired",
			policy: &CachingPolicy{
				LocalDate:         time.Now(),
				FreshnessLifetime: 3600,
			},
			wantFresh: true,
		},
		{
			name: "stale — lifetime expired",
			policy: &CachingPolicy{
				LocalDate:         time.Now().Add(-2 * time.Hour),
				FreshnessLifetime: 3600,
			},
			wantFresh: false,
		},
		{
			name: "zero lifetime — stale immediately",
			policy: &CachingPolicy{
				LocalDate:         time.Now().Add(-time.Second),
				FreshnessLifetime: 0,
			},
			wantFresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := proxyRequest{cachingPolicy: tt.policy}
			if got := pr.checkCacheFreshness(); got != tt.wantFresh {
				t.Errorf("expected %t got %t", tt.wantFresh, got)
			}
		})
	}
}

func TestParseRequestRanges(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameRange, "bytes=0-10")

	o := &bo.Options{MultipartRangesDisabled: true}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:         r,
		rsc:             request.GetResources(r),
		upstreamRequest: r,
	}
	pr.parseRequestRanges()

	if len(pr.wantedRanges) == 0 {
		t.Errorf("unexpected range parse: %v", pr.wantedRanges)
	}

	r.Header.Set(headers.NameRange, "bytes=0-10,12-20")
	pr.parseRequestRanges()

	if pr.wantedRanges != nil {
		t.Errorf("unexpected nil got %s", pr.wantedRanges.String())
	}
}

func TestStripConditionalHeaders(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameIfNoneMatch, "test")
	pr := proxyRequest{
		upstreamRequest: r,
		cachingPolicy:   &CachingPolicy{IsClientConditional: true},
	}
	pr.stripConditionalHeaders()
	if v := r.Header.Get(headers.NameIfNoneMatch); v == "test" {
		t.Errorf("expected header to be stripped: %s", headers.NameIfNoneMatch)
	}
}

func TestSetBodyWriter(t *testing.T) {
	buff := make([]byte, 0)
	pr := proxyRequest{
		writeToCache:     true,
		contentLength:    -1,
		responseWriter:   bytes.NewBuffer(buff),
		upstreamResponse: &http.Response{StatusCode: http.StatusOK},
		cachingPolicy:    &CachingPolicy{},
		mapLock:          &sync.Mutex{},
	}

	PrepareResponseWriter(pr.responseWriter, pr.upstreamResponse.StatusCode, pr.upstreamResponse.Header)

	pr.setBodyWriter()
	if pr.cacheBuffer == nil {
		t.Error("expected non-nil cacheBody")
	}

	pr.cachingPolicy.IsClientFresh = true
	pr.cacheBuffer = nil
	pr.upstreamResponse.StatusCode = http.StatusNotModified

	pr.setBodyWriter()
	if pr.cacheBuffer == nil {
		t.Error("expected non-nil cacheBody")
	}
}

func TestWriteResponseBody(t *testing.T) {
	pr := proxyRequest{}
	pr.writeResponseBody()
	if pr.responseWriter != nil {
		t.Error("expected nil writer")
	}
}

func TestDetermineCacheability(t *testing.T) {
	logger.SetLogger(testLogger)

	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider",
		"test",
	})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1", nil)
	r = request.SetResources(r, request.NewResources(nil, nil, cache.Configuration(),
		cache, nil, nil))

	pr := proxyRequest{
		Request:       r,
		rsc:           request.GetResources(r),
		cachingPolicy: &CachingPolicy{NoCache: true, LastModified: time.Unix(1, 0)},
		writeToCache:  true,
		cacheDocument: &HTTPDocument{
			CachingPolicy: &CachingPolicy{},
		},
	}
	pr.determineCacheability()
	if pr.writeToCache {
		t.Errorf("expected %t got %t", false, pr.writeToCache)
	}

	pr.revalidation = RevalStatusLocal
	pr.cacheDocument.CachingPolicy.LastModified = time.Unix(0, 0)
	pr.cachingPolicy.NoCache = false
	pr.cachingPolicy.HasIfModifiedSince = true
	pr.determineCacheability()

	if pr.cacheStatus != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, pr.cacheStatus)
	}
}

func TestStoreNoWrite(t *testing.T) {
	pr := proxyRequest{}
	err := pr.store()
	if err != nil {
		t.Error(err)
	}
}

func TestUpdateContentLengthNilResponse(t *testing.T) {
	pr := proxyRequest{contentLength: -1}
	pr.updateContentLength()
	if pr.contentLength != -1 {
		t.Errorf("expected %d got %d", -1, pr.contentLength)
	}
}

func TestPrepareResponse(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameRange, "bytes=0-10")

	o := &bo.Options{}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:          r,
		rsc:              request.GetResources(r),
		cachingPolicy:    &CachingPolicy{},
		upstreamResponse: &http.Response{StatusCode: http.StatusOK},
		cacheDocument:    &HTTPDocument{},
	}
	pr.parseRequestRanges()

	pr.cacheDocument.Ranges = pr.wantedRanges

	if !pr.wantsRanges || len(pr.wantedRanges) < 1 {
		t.Errorf("unexpected range parse: %v", pr.wantedRanges)
	}

	pr.prepareResponse()

	// test again with full body and needed ranges
	pr.upstreamResponse.StatusCode = http.StatusOK
	pr.cacheStatus = status.LookupStatusKeyMiss
	pr.writeToCache = true
	pr.upstreamReader = bytes.NewBufferString("trickster")
	headers.Merge(pr.upstreamResponse.Header, http.Header{
		headers.NameContentRange: {"bytes 0-9"},
	})

	pr.prepareResponse()

	if pr.upstreamResponse.StatusCode != http.StatusPartialContent {
		t.Errorf("expected %d got %d", http.StatusPartialContent, pr.upstreamResponse.StatusCode)
	}

	// test again with no ranges
	pr.wantsRanges = false
	pr.wantedRanges = nil
	pr.prepareResponse()
}

type truncatingReader struct {
	partial []byte
	err     error
	done    bool
}

func (r *truncatingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	n := copy(p, r.partial)
	r.done = true
	return n, nil
}

func TestPrepareResponse_UpstreamReadErrorSkipsCache(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameRange, "bytes=0-10")

	o := &bo.Options{}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:          r,
		rsc:              request.GetResources(r),
		cachingPolicy:    &CachingPolicy{},
		upstreamResponse: &http.Response{StatusCode: http.StatusOK},
		cacheDocument:    &HTTPDocument{},
	}
	pr.parseRequestRanges()
	pr.cacheDocument.Ranges = pr.wantedRanges

	pr.cacheStatus = status.LookupStatusKeyMiss
	pr.writeToCache = true
	pr.upstreamReader = &truncatingReader{
		partial: []byte("trunc"),
		err:     io.ErrUnexpectedEOF,
	}
	headers.Merge(pr.upstreamResponse.Header, http.Header{
		headers.NameContentRange: {"bytes 0-99"},
	})

	pr.prepareResponse()

	if pr.writeToCache {
		t.Error("writeToCache must be cleared after upstream read error")
	}
	if pr.cacheDocument != nil && pr.cacheDocument.isLoaded {
		t.Error("cacheDocument.isLoaded must not be true when upstream read errored")
	}
}

func TestPrepareResponsePreconditionFailed(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	pr := proxyRequest{
		Request: r,
		cachingPolicy: &CachingPolicy{
			IsClientConditional: true,
			IsClientFresh:       true,
			HasIfNoneMatch:      true,
			IfNoneMatchResult:   false,
			ETag:                "1234",
			IfNoneMatchValue:    "1234",
		},
		upstreamResponse: &http.Response{},
		cacheDocument:    &HTTPDocument{},
	}
	pr.Method = http.MethodPost
	pr.prepareResponse()
	if pr.upstreamResponse.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("expected %d got %d", http.StatusPreconditionFailed, pr.upstreamResponse.StatusCode)
	}
}

func TestPrepareRevalidationRequest(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameRange, "bytes=0-10,12-20")

	o := &bo.Options{DearticulateUpstreamRanges: true}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:          r,
		rsc:              request.GetResources(r),
		upstreamRequest:  r,
		cachingPolicy:    &CachingPolicy{},
		upstreamResponse: &http.Response{},
		cacheDocument:    &HTTPDocument{Ranges: byterange.Ranges{byterange.Range{Start: 30, End: 40}}},
		cacheStatus:      status.LookupStatusPartialHit,
		wantedRanges:     byterange.Ranges{{Start: 0, End: 10}, {Start: 12, End: 20}},
	}
	pr.prepareRevalidationRequest()

	v := pr.revalidationRequest.Header.Get(headers.NameRange)
	expected := pr.cacheDocument.Ranges.String()

	if v != expected {
		t.Errorf("expected %s got %s", expected, v)
	}
}

func TestPrepareRevalidationRequestNoRange(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameRange, "bytes=0-10,12-20")

	o := &bo.Options{DearticulateUpstreamRanges: true}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:          r,
		rsc:              request.GetResources(r),
		upstreamRequest:  r,
		cachingPolicy:    &CachingPolicy{},
		upstreamResponse: &http.Response{},
		cacheDocument:    &HTTPDocument{}, // Ranges: byterange.Ranges{byterange.Range{Start: 30, End: 40}}},
		cacheStatus:      status.LookupStatusPartialHit,
		wantedRanges:     byterange.Ranges{{Start: 0, End: 10}, {Start: 12, End: 20}},
	}
	pr.prepareRevalidationRequest()

	v := pr.revalidationRequest.Header.Get(headers.NameRange)
	if v != "" {
		t.Errorf("expected empty string got %s", v)
	}
}

func TestPrepareRevalidationRequestDefaultRangeInclusiveEnd(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)

	o := &bo.Options{DearticulateUpstreamRanges: true}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:          r,
		rsc:              request.GetResources(r),
		upstreamRequest:  r,
		cachingPolicy:    &CachingPolicy{},
		upstreamResponse: &http.Response{},
		cacheDocument: &HTTPDocument{
			ContentLength: 100,
			Ranges:        byterange.Ranges{byterange.Range{Start: 30, End: 40}},
		},
		cacheStatus: status.LookupStatusPartialHit,
		// no wantedRanges — triggers default range using ContentLength
	}
	pr.prepareRevalidationRequest()

	// with no wantedRanges, the default wanted range is 0 to ContentLength-1 (99)
	// revalRanges = neededRanges.CalculateDeltas(wr, cl) with nil neededRanges gives wr back
	// that's 1 range so rh = revalRanges.String() = "bytes=0-99"
	v := pr.revalidationRequest.Header.Get(headers.NameRange)
	expected := "bytes=0-99"
	if v != expected {
		t.Errorf("expected %s got %s", expected, v)
	}
}

func TestPrepareUpstreamRequests(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r.Header.Set(headers.NameRange, "bytes=0-10,12-20")

	o := &bo.Options{DearticulateUpstreamRanges: true}
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))

	pr := proxyRequest{
		Request:          r,
		rsc:              request.GetResources(r),
		upstreamRequest:  r,
		cachingPolicy:    &CachingPolicy{},
		upstreamResponse: &http.Response{},
		cacheDocument:    &HTTPDocument{Ranges: byterange.Ranges{byterange.Range{Start: 30, End: 40}}},
		cacheStatus:      status.LookupStatusPartialHit,
		wantedRanges:     byterange.Ranges{{Start: 0, End: 10}, {Start: 12, End: 20}},
		neededRanges:     byterange.Ranges{{Start: 0, End: 10}, {Start: 12, End: 20}},
	}

	pr.prepareUpstreamRequests()

	expected := 2
	v := len(pr.originRequests)

	if v != expected {
		t.Errorf("expected %d got %d", expected, v)
	}
}

func TestStoreTrueContentType(t *testing.T) {
	ts, _, r, _, _ := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	defer ts.Close()

	expected := "1234"

	pr := newProxyRequest(r, nil)
	pr.cachingPolicy = &CachingPolicy{NoCache: true, LastModified: time.Unix(1, 0)}
	pr.writeToCache = true
	pr.cacheDocument = &HTTPDocument{
		CachingPolicy: &CachingPolicy{},
		ContentType:   "5678",
	}
	pr.trueContentType = expected

	err := pr.store()
	if err != nil {
		t.Error(err)
	}

	if pr.cacheDocument.ContentType != expected {
		t.Errorf("expected %s got %s", expected, pr.cacheDocument.ContentType)
	}
}

func TestReconstituteResponses(t *testing.T) {
	pr := &proxyRequest{}

	pr.reconstituteResponses()
	if len(pr.originRequests) != 0 {
		t.Errorf("expected %d got %d", 0, len(pr.originRequests))
	}
}

// errReader is an io.ReadCloser that always returns an error
type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }
func (e *errReader) Close() error             { return nil }

func TestReconstituteResponsesReadError(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	r1, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	r2, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	baseReq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	baseReq = request.SetResources(baseReq, request.NewResources(&bo.Options{}, nil, nil, nil, nil, nil))

	readErr := errors.New("simulated read error")

	pr := &proxyRequest{
		mapLock:        &sync.Mutex{},
		rsc:            request.GetResources(baseReq),
		cachingPolicy:  &CachingPolicy{},
		originRequests: []*http.Request{r1, r2},
		originResponses: []*http.Response{
			{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{headers.NameContentRange: []string{"bytes 0-3/10"}},
				Body:       &errReader{err: readErr},
			},
			{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{headers.NameContentRange: []string{"bytes 4-9/10"}},
				Body:       io.NopCloser(bytes.NewReader([]byte("456789"))),
			},
		},
		cacheDocument: &HTTPDocument{ContentLength: 10},
	}
	pr.Request = baseReq

	// should not panic; the error reader's goroutine logs and returns
	pr.reconstituteResponses()

	// the second response should still have been processed
	if pr.upstreamResponse == nil {
		t.Error("expected upstream response to be set")
	}
}

func TestReconstituteResponsesRevalidationReadError(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	r1, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	reval, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	baseReq, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	baseReq = request.SetResources(baseReq, request.NewResources(&bo.Options{}, nil, nil, nil, nil, nil))

	readErr := errors.New("simulated revalidation read error")

	pr := &proxyRequest{
		mapLock:             &sync.Mutex{},
		rsc:                 request.GetResources(baseReq),
		cachingPolicy:       &CachingPolicy{},
		revalidationRequest: reval,
		revalidationResponse: &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{headers.NameContentRange: []string{"bytes 0-4/10"}},
			Body:       &errReader{err: readErr},
		},
		originRequests: []*http.Request{r1},
		originResponses: []*http.Response{
			{
				StatusCode: http.StatusPartialContent,
				Header:     http.Header{headers.NameContentRange: []string{"bytes 5-9/10"}},
				Body:       io.NopCloser(bytes.NewReader([]byte("56789"))),
			},
		},
		cacheDocument: &HTTPDocument{ContentLength: 10},
	}
	pr.Request = baseReq

	// should not panic; the revalidation error reader's goroutine logs and returns
	pr.reconstituteResponses()

	if pr.upstreamResponse == nil {
		t.Error("expected upstream response to be set")
	}
}
