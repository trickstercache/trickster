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
	"errors"
	"net/http"
	"sync"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// errTransport is an http.RoundTripper that always returns an error. It lets
// the test drive PrepareFetchReader down its upstream-unreachable path
// without spinning up an httptest.Server.
type errTransport struct{ err error }

func (e *errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

// TestMakeUpstreamRequestsCapturesFetchFailure exercises the parallel fetch
// path in makeUpstreamRequests where PrepareFetchReader's third return value
// (contentLength) used to be silently discarded. With the failure-capture
// edit in place, the goroutine still populates originResponses with the 502
// fallback and originReaders with nil; downstream reconstituteResponses must
// not nil-deref when iterating the slice.
func TestMakeUpstreamRequestsCapturesFetchFailure(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	o := &bo.Options{
		HTTPClient: &http.Client{
			Transport: &errTransport{err: errors.New("upstream unreachable")},
		},
	}

	mkReq := func(path string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		return request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))
	}

	// Force the parallel path (>1 origin requests). The single-request
	// short-circuit goes through a different code path (makeSimpleUpstreamRequests).
	r1 := mkReq("/a")
	r2 := mkReq("/b")
	base := mkReq("/")

	pr := &proxyRequest{
		Request:         base,
		rsc:             request.GetResources(base),
		upstreamRequest: base,
		mapLock:         &sync.Mutex{},
		originRequests:  []*http.Request{r1, r2},
	}

	if err := pr.makeUpstreamRequests(); err != nil {
		t.Fatalf("makeUpstreamRequests returned error: %v", err)
	}

	if got := len(pr.originResponses); got != 2 {
		t.Fatalf("expected 2 originResponses, got %d", got)
	}
	for i, resp := range pr.originResponses {
		if resp == nil {
			t.Fatalf("originResponses[%d] is nil; downstream will nil-deref", i)
		}
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("originResponses[%d].StatusCode = %d, want 502",
				i, resp.StatusCode)
		}
	}
	// Readers must be nil on failure (the upstream error path) -- the
	// captured-but-logged contract is what we're verifying. Downstream
	// must check reader for nil before using it.
	for i, reader := range pr.originReaders {
		if reader != nil {
			t.Errorf("originReaders[%d] expected nil on fetch failure, got %T", i, reader)
		}
	}
}

// TestMakeUpstreamRequestsRevalidationCapturesFetchFailure covers the
// revalidation branch (proxy_request.go:319) where the contentLength was
// discarded. Same shape, different code path.
func TestMakeUpstreamRequestsRevalidationCapturesFetchFailure(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	o := &bo.Options{
		HTTPClient: &http.Client{
			Transport: &errTransport{err: errors.New("upstream unreachable")},
		},
	}

	mkReq := func(path string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		return request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))
	}

	reval := mkReq("/reval")
	origin := mkReq("/origin")
	base := mkReq("/")

	pr := &proxyRequest{
		Request:             base,
		rsc:                 request.GetResources(base),
		upstreamRequest:     base,
		mapLock:             &sync.Mutex{},
		revalidationRequest: reval,
		originRequests:      []*http.Request{origin},
	}

	if err := pr.makeUpstreamRequests(); err != nil {
		t.Fatalf("makeUpstreamRequests returned error: %v", err)
	}

	if pr.revalidationResponse == nil {
		t.Fatal("revalidationResponse is nil; downstream will nil-deref on StatusCode read")
	}
	if pr.revalidationResponse.StatusCode != http.StatusBadGateway {
		t.Errorf("revalidationResponse.StatusCode = %d, want 502",
			pr.revalidationResponse.StatusCode)
	}
	if pr.revalidationReader != nil {
		t.Errorf("revalidationReader expected nil on fetch failure, got %T",
			pr.revalidationReader)
	}
}
