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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestCollapseEligible(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	authed := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	authed.Header.Set(headers.NameAuthorization, "Bearer x")
	keyed := po.New()
	keyed.CacheKeyHeaders = []string{"Accept-Encoding"}

	tests := []struct {
		name     string
		r        *http.Request
		code     int
		h        http.Header
		pc       *po.Options
		expected bool
	}{
		{"plain 200", get, 200, http.Header{}, po.New(), true},
		{"non-200", get, 206, http.Header{}, po.New(), false},
		{"set-cookie", get, 200,
			http.Header{headers.NameSetCookie: {"a=1"}}, po.New(), false},
		{"private", get, 200,
			http.Header{headers.NameCacheControl: {"private, max-age=60"}}, po.New(), false},
		{"no-store", get, 200,
			http.Header{headers.NameCacheControl: {"no-store"}}, po.New(), false},
		{"sse", get, 200,
			http.Header{headers.NameContentType: {"text/event-stream"}}, po.New(), false},
		{"authorized without public", authed, 200, http.Header{}, po.New(), false},
		{"authorized with public", authed, 200,
			http.Header{headers.NameCacheControl: {"public, max-age=60"}}, po.New(), true},
		{"authorized with s-maxage", authed, 200,
			http.Header{headers.NameCacheControl: {"s-maxage=30"}}, po.New(), true},
		{"vary star", get, 200,
			http.Header{headers.NameVary: {"*"}}, po.New(), false},
		{"vary unkeyed", get, 200,
			http.Header{headers.NameVary: {"Accept-Encoding"}}, po.New(), false},
		{"vary keyed", get, 200,
			http.Header{headers.NameVary: {"accept-encoding"}}, keyed, true},
		{"vary partially keyed", get, 200,
			http.Header{headers.NameVary: {"Accept-Encoding, Origin"}}, keyed, false},
		{"vary nil pathconfig", get, 200,
			http.Header{headers.NameVary: {"Accept-Encoding"}}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := collapseEligible(tc.r, tc.code, tc.h, tc.pc); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

// collapseHarness routes through CollapsedPassthrough to a real passthrough
// handler pointed at originURL, with Resources installed per request.
func collapseHarness(t *testing.T, originURL string) *httptest.Server {
	t.Helper()
	u, err := url.Parse(originURL)
	if err != nil {
		t.Fatal(err)
	}
	o := bo.New()
	o.Name = "test"
	o.Provider = "rp"
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.PathPrefix = ""
	client, err := NewTestClient("test", o, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.HTTPClient = client.HTTPClient()
	h := CollapsedPassthrough(NewPassthroughHandler(client))
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.NewResources(o, po.New(), nil, nil, client, nil)
		h.ServeHTTP(w, request.SetResources(r, rsc))
	}))
	t.Cleanup(front.Close)
	return front
}

func TestCollapsedPassthroughSharesOneFetch(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	body := strings.Repeat("x", 4*HTTPBlockSize)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set(headers.NameContentLength, fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body[:10]))
		http.NewResponseController(w).Flush()
		<-release // hold the stream open so followers can join
		w.Write([]byte(body[10:]))
	}))
	defer origin.Close()
	front := collapseHarness(t, origin.URL)

	const clients = 4
	var wg sync.WaitGroup
	results := make([]string, clients)
	errs := make([]error, clients)
	for i := range clients {
		wg.Go(func() {
			resp, err := http.Get(front.URL + "/obj")
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			results[i] = string(b)
			errs[i] = err
		})
		// stagger so the first request leads and the rest join
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Errorf("expected exactly 1 upstream fetch, got %d", got)
	}
	for i := range clients {
		if errs[i] != nil {
			t.Errorf("client %d: %v", i, errs[i])
		}
		if results[i] != body {
			t.Errorf("client %d: body mismatch (len %d, want %d)", i, len(results[i]), len(body))
		}
	}
}

func TestCollapsedPassthroughRefusesPrivate(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set(headers.NameCacheControl, "private")
		w.Write([]byte("secret"))
	}))
	defer origin.Close()
	front := collapseHarness(t, origin.URL)

	for range 2 {
		resp, err := http.Get(front.URL + "/priv")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "secret" {
			t.Errorf("expected body delivery for ineligible response, got %q", b)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 independent fetches for a private response, got %d", got)
	}
}

func TestCollapsedPassthroughPostBypasses(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte("ok"))
	}))
	defer origin.Close()
	front := collapseHarness(t, origin.URL)

	for range 2 {
		resp, err := http.Post(front.URL+"/p", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("POST must never collapse; expected 2 fetches, got %d", got)
	}
}

func TestCollapsedPassthroughUnknownLength(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		// no Content-Length: chunked
		w.Write([]byte(strings.Repeat("y", 3*HTTPBlockSize)))
	}))
	defer origin.Close()
	front := collapseHarness(t, origin.URL)

	resp, err := http.Get(front.URL + "/chunked")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 3*HTTPBlockSize {
		t.Errorf("expected full chunked body through collapse, got %d bytes", len(b))
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 fetch, got %d", got)
	}
}

func TestCollapsedPassthroughTruncationFansOut(t *testing.T) {
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameContentLength, "4096")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		http.NewResponseController(w).Flush()
		<-release
		// close without sending the remaining bytes
		panic(http.ErrAbortHandler)
	}))
	defer origin.Close()
	front := collapseHarness(t, origin.URL)

	const clients = 2
	var wg sync.WaitGroup
	failures := make([]bool, clients)
	for i := range clients {
		wg.Go(func() {
			resp, err := http.Get(front.URL + "/trunc")
			if err != nil {
				failures[i] = true
				return
			}
			defer resp.Body.Close()
			if _, err := io.ReadAll(resp.Body); err != nil {
				failures[i] = true
			}
		})
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, failed := range failures {
		if !failed {
			t.Errorf("client %d: truncated collapse presented as a complete response", i)
		}
	}
}

func TestPCFGrowthCapAborts(t *testing.T) {
	resp := &http.Response{}
	pcf := NewPCF(resp, -1, int64(HTTPBlockSize))
	if pcf == nil {
		t.Fatal("expected growable PCF")
	}
	if _, err := pcf.Write(make([]byte, HTTPBlockSize)); err != nil {
		t.Fatalf("write within cap failed: %v", err)
	}
	if _, err := pcf.Write([]byte("overflow")); err == nil {
		t.Error("expected an error writing past the growth cap")
	}
}

func TestPCFCloseWithErrorReachesClients(t *testing.T) {
	resp := &http.Response{}
	pcf := NewPCF(resp, -1, int64(HTTPBlockSize*4))
	pcf.Write([]byte("partial"))
	done := make(chan error, 1)
	go func() {
		done <- pcf.AddClient(io.Discard)
	}()
	pcf.CloseWithError(io.ErrUnexpectedEOF)
	select {
	case err := <-done:
		if err != io.ErrUnexpectedEOF {
			t.Errorf("expected ErrUnexpectedEOF to reach the client, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client never released after CloseWithError")
	}
}
