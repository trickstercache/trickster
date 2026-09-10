/*
 * Copyright 2026 The Trickster Authors
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
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"

	"github.com/stretchr/testify/require"
)

func upstreamResources(pc *po.Options) *request.Resources {
	if pc != nil {
		_ = pc.Initialize("")
	}
	return request.NewResources(&bo.Options{Name: "test"}, pc, nil, nil, nil, nil)
}

func okResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

func TestDoUpstreamWithoutPolicy(t *testing.T) {
	var calls int
	do := func(_ *http.Request) (*http.Response, error) { calls++; return okResponse("ok"), nil }
	r := httptest.NewRequest(http.MethodGet, "http://origin/", nil)
	for _, rsc := range []*request.Resources{nil, upstreamResources(nil), upstreamResources(po.New())} {
		resp, err := doUpstream(do, r, rsc)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	require.Equal(t, 3, calls)
}

func TestDoUpstreamRetriesStatusAndErrors(t *testing.T) {
	pc := po.New()
	pc.Retry = &po.RetryOptions{Attempts: 3, Codes: []int{http.StatusBadGateway}, BudgetPercent: 100}
	rsc := upstreamResources(pc)
	var calls int
	do := func(_ *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("connection refused")
		case 2:
			return &http.Response{StatusCode: http.StatusBadGateway,
				Body: io.NopCloser(strings.NewReader("bad"))}, nil
		}
		return okResponse("ok"), nil
	}
	r := httptest.NewRequest(http.MethodGet, "http://origin/", nil)
	resp, err := doUpstream(do, r, rsc)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 3, calls)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "ok", string(body))
	require.NoError(t, resp.Body.Close())

	// attempts exhausted: the last outcome is returned
	calls = 0
	pc.Retry.Initialize() // a fresh budget
	do = func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody}, nil
	}
	resp, err = doUpstream(do, r, rsc)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.Equal(t, 4, calls)

	calls = 0
	pc.Retry.Initialize()
	do = func(_ *http.Request) (*http.Response, error) { calls++; return nil, errors.New("down") }
	_, err = doUpstream(do, r, rsc)
	require.EqualError(t, err, "down")
	require.Equal(t, 4, calls)

	// a status the path does not retry is returned at once
	calls = 0
	do = func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody}, nil
	}
	resp, err = doUpstream(do, r, rsc)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, 1, calls)
}

func TestDoUpstreamRetryEligibility(t *testing.T) {
	pc := po.New()
	pc.Retry = &po.RetryOptions{Attempts: 2, BudgetPercent: 100}
	rsc := upstreamResources(pc)
	var calls int
	do := func(_ *http.Request) (*http.Response, error) { calls++; return nil, errors.New("down") }

	// a non-idempotent method is never retried
	r := httptest.NewRequest(http.MethodPost, "http://origin/", strings.NewReader("body"))
	_, err := doUpstream(do, r, rsc)
	require.Error(t, err)
	require.Equal(t, 1, calls)

	// an idempotent method with a body needs GetBody to replay it
	calls = 0
	r = httptest.NewRequest(http.MethodPut, "http://origin/", strings.NewReader("body"))
	_, err = doUpstream(do, r, rsc)
	require.Error(t, err)
	require.Equal(t, 1, calls)

	calls = 0
	var bodies []string
	do = func(req *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(b))
		return nil, errors.New("down")
	}
	r = httptest.NewRequest(http.MethodPut, "http://origin/", strings.NewReader("first"))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("replayed")), nil }
	_, err = doUpstream(do, r, rsc)
	require.Error(t, err)
	require.Equal(t, []string{"first", "replayed", "replayed"}, bodies)

	// a body that cannot be replayed ends the attempts
	calls = 0
	r.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("gone") }
	r.Body = io.NopCloser(strings.NewReader("first"))
	_, err = doUpstream(do, r, rsc)
	require.Error(t, err)
	require.Equal(t, 1, calls)

	// a request the client abandoned is not retried
	calls = 0
	ctx, cancel := context.WithCancel(context.Background())
	r = httptest.NewRequest(http.MethodGet, "http://origin/", nil).WithContext(ctx)
	do = func(_ *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	}
	_, err = doUpstream(do, r, rsc)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

func TestDoUpstreamBudget(t *testing.T) {
	pc := po.New()
	pc.Retry = &po.RetryOptions{Attempts: 1, BudgetPercent: 1}
	rsc := upstreamResources(pc)
	var calls int
	do := func(_ *http.Request) (*http.Response, error) { calls++; return nil, errors.New("down") }
	r := httptest.NewRequest(http.MethodGet, "http://origin/", nil)
	// the minimum retries are spent, then the budget refuses
	for range 3 {
		_, _ = doUpstream(do, r, rsc)
	}
	require.Equal(t, 6, calls)
	_, _ = doUpstream(do, r, rsc)
	require.Equal(t, 7, calls)
}

func TestDoUpstreamBackoffAndTimeouts(t *testing.T) {
	pc := po.New()
	pc.Retry = &po.RetryOptions{Attempts: 5, BudgetPercent: 100, Backoff: timeconv.Duration(20 * time.Millisecond)}
	pc.Timeout = timeconv.Duration(50 * time.Millisecond)
	rsc := upstreamResources(pc)
	var calls int
	do := func(_ *http.Request) (*http.Response, error) { calls++; return nil, errors.New("down") }
	r := httptest.NewRequest(http.MethodGet, "http://origin/", nil)
	start := time.Now()
	_, err := doUpstream(do, r, rsc)
	require.Error(t, err)
	require.Less(t, calls, 6, "the path timeout ends the attempts before they are exhausted")
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)

	// a backoff cut short by the deadline reports the deadline
	pc.Retry.Codes = []int{http.StatusBadGateway}
	pc.Retry.Initialize()
	do = func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody}, nil
	}
	_, err = doUpstream(do, r, rsc)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// the attempt deadline reaches each attempt and is released with the body
	pc = po.New()
	pc.AttemptTimeout = timeconv.Duration(30 * time.Millisecond)
	pc.Timeout = timeconv.Duration(time.Second)
	rsc = upstreamResources(pc)
	var seen context.Context
	do = func(req *http.Request) (*http.Response, error) {
		seen = req.Context()
		return okResponse("ok"), nil
	}
	resp, err := doUpstream(do, r, rsc)
	require.NoError(t, err)
	deadline, ok := seen.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(30*time.Millisecond), deadline, 20*time.Millisecond)
	require.NoError(t, resp.Body.Close())
	require.ErrorIs(t, seen.Err(), context.Canceled)

	// a nil body still carries the releases
	do = func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent}, nil
	}
	resp, err = doUpstream(do, r, rsc)
	require.NoError(t, err)
	require.NotNil(t, resp.Body)
	require.NoError(t, resp.Body.Close())
}

func TestForwardTrailers(t *testing.T) {
	w := httptest.NewRecorder()
	forwardTrailers(w, nil)
	forwardTrailers(&bytes.Buffer{}, &http.Response{Trailer: http.Header{"Grpc-Status": {"0"}}})
	forwardTrailers(w, &http.Response{Trailer: http.Header{"Grpc-Status": {"0"}, "Grpc-Message": {"ok"}}})
	require.Equal(t, "0", w.Header().Get(http.TrailerPrefix+"Grpc-Status"))
	require.Equal(t, "ok", w.Header().Get(http.TrailerPrefix+"Grpc-Message"))
}

func TestDoProxyForwardsTrailers(t *testing.T) {
	var sawTrailerRequest atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTrailerRequest.Store(r.Header.Get(headers.NameTe) == "trailers")
		w.Header().Set(headers.NameTrailer, "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
		w.Header().Set("Grpc-Status", "0")
	}))
	defer origin.Close()
	o := bo.New()
	o.Name = "test"
	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	serve := func(forward bool) *http.Response {
		pc := po.New()
		pc.ForwardTrailers = forward
		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(tc.WithResources(r.Context(),
				request.NewResources(o, pc, nil, nil, nil, tu.NewTestTracer())))
			r.URL, _ = url.Parse(origin.URL + r.URL.Path)
			DoProxy(w, r, true)
		}))
		defer front.Close()
		req, err := http.NewRequest(http.MethodGet, front.URL+"/rpc", nil)
		require.NoError(t, err)
		req.Header.Set(headers.NameTe, "trailers")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, "body", string(b))
		return resp
	}
	resp := serve(true)
	require.Equal(t, "0", resp.Trailer.Get("Grpc-Status"))
	require.True(t, sawTrailerRequest.Load(), "the client's request for trailers reaches the origin")
	resp = serve(false)
	require.Empty(t, resp.Trailer.Get("Grpc-Status"))
	require.False(t, sawTrailerRequest.Load())
}

func TestObjectProxyCacheForwardsTrailers(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameTrailer, "Grpc-Status")
		w.Header().Set(headers.NameCacheControl, "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
		w.Header().Set("Grpc-Status", "0")
	}))
	defer origin.Close()
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, nil)
	require.NoError(t, err)
	defer closeTestHarness(ts, r)
	pc := rsc.PathConfig
	pc.ForwardTrailers = true
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		req := r.Clone(in.Context())
		req.URL, _ = url.Parse(origin.URL + "/rpc")
		ObjectProxyCacheRequest(w, request.SetResources(req, rsc))
	}))
	defer front.Close()
	resp, err := http.Get(front.URL + "/rpc")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "body", string(b))
	require.Equal(t, "0", resp.Trailer.Get("Grpc-Status"))
}

func TestUpstreamPolicyLeavesUpgradesAlone(t *testing.T) {
	// a tunnel outlives the exchange that opened it: an upgrade request is neither bounded
	// nor retried, and its body keeps the read/write contract the reverse proxy tunnels over
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = brw.Flush()
		// the tunnel stays open longer than any deadline the path sets
		time.Sleep(150 * time.Millisecond)
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = brw.WriteString("echo:" + line)
		_ = brw.Flush()
	}))
	defer origin.Close()
	for _, pc := range []*po.Options{
		func() *po.Options {
			p := po.New()
			p.Timeout = timeconv.Duration(50 * time.Millisecond)
			return p
		}(),
		func() *po.Options {
			p := po.New()
			p.Retry = &po.RetryOptions{Attempts: 2, Codes: []int{http.StatusSwitchingProtocols}}
			return p
		}(),
	} {
		_ = pc.Initialize("")
		u, _ := url.Parse(origin.URL)
		o := bo.New()
		o.Name, o.Provider, o.Scheme, o.Host, o.PathPrefix = "test", "rp", u.Scheme, u.Host, ""
		client, err := NewTestClient("test", o, nil, nil, nil)
		require.NoError(t, err)
		h := NewPassthroughHandler(client)
		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, request.SetResources(r, request.NewResources(o, pc, nil, nil, client, nil)))
		}))
		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(front.URL, "http://"), 5*time.Second)
		require.NoError(t, err)
		require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
		_, err = conn.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
		require.NoError(t, err)
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		time.Sleep(200 * time.Millisecond)
		_, err = conn.Write([]byte("ping\n"))
		require.NoError(t, err)
		got, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "echo:ping\n", got, "the tunnel carries bytes after the path deadline")
		conn.Close()
		front.Close()
	}

	// an origin that upgrades unasked hands the body back untouched
	pc := po.New()
	pc.Timeout = timeconv.Duration(time.Second)
	rsc := upstreamResources(pc)
	body := &bytes.Buffer{}
	do := func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: rwBody{body}}, nil
	}
	resp, err := doUpstream(do, httptest.NewRequest(http.MethodGet, "http://origin/", nil), rsc)
	require.NoError(t, err)
	_, ok := resp.Body.(io.ReadWriteCloser)
	require.True(t, ok, "a switched protocol keeps its read/write body")
}

type rwBody struct{ *bytes.Buffer }

func (rwBody) Close() error { return nil }

func TestRetryDoesNotWaitOnAStalledAttempt(t *testing.T) {
	// an origin that sends retryable headers and then stalls its body is closed at once and
	// retried, rather than drained while nothing arrives
	var calls atomic.Int32
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			http.NewResponseController(w).Flush()
			<-release
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()
	defer close(release)
	pc := po.New()
	pc.Retry = &po.RetryOptions{Attempts: 1, Codes: []int{http.StatusServiceUnavailable}, BudgetPercent: 100}
	rsc := upstreamResources(pc)
	r := httptest.NewRequest(http.MethodGet, origin.URL+"/", nil)
	r.RequestURI = ""
	start := time.Now()
	resp, err := doUpstream(http.DefaultClient.Do, r, rsc)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Less(t, time.Since(start), 2*time.Second)
	require.EqualValues(t, 2, calls.Load())
}
