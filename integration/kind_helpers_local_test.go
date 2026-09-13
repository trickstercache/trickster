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

package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartLoadCountsTruncatedBodies(t *testing.T) {
	// a 200 whose body ends before its declared length is a failure the client only sees
	// while reading the body, and the load loop must count it as one
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("0123456789"))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(cut.Close)
	whole := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(whole.Close)
	client := &http.Client{Timeout: 5 * time.Second}

	load := startLoad(1, func() (*http.Response, error) { return client.Get(cut.URL) })
	require.Eventually(t, func() bool { return load.requests.Load() >= 3 }, 10*time.Second, 10*time.Millisecond)
	requests, errors, failures := load.end()
	require.Equal(t, requests, errors, "every truncated body is an error: %s", failures)
	require.Contains(t, failures.String(), "body:")

	load = startLoad(1, func() (*http.Response, error) { return client.Get(whole.URL) })
	require.Eventually(t, func() bool { return load.requests.Load() >= 3 }, 10*time.Second, 10*time.Millisecond)
	_, errors, failures = load.end()
	require.Zero(t, errors, "a complete body is not an error: %s", failures)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)
	load = startLoad(1, func() (*http.Response, error) { return client.Get(bad.URL) })
	require.Eventually(t, func() bool { return load.requests.Load() >= 3 }, 10*time.Second, 10*time.Millisecond)
	requests, errors, failures = load.end()
	require.Equal(t, requests, errors, "a bad status is counted once: %s", failures)
	require.NotContains(t, failures.String(), "body:")
}

func TestScrapeMetrics(t *testing.T) {
	const text = `# HELP trickster_kgw_reconcile_errors_total errors by stage
# TYPE trickster_kgw_reconcile_errors_total counter
trickster_kgw_reconcile_errors_total{stage="translate"} 2
trickster_kgw_reconcile_errors_total{stage="apply"} 3
go_goroutines 41
process_open_fds 17
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(text))
	}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	snap, err := scrapeMetrics(addr)
	require.NoError(t, err)

	total, ok := snap.sum("trickster_kgw_reconcile_errors_total")
	require.True(t, ok)
	assert.InDelta(t, 5.0, total, 0, "every labeled series is summed")
	v, ok := snap.value("trickster_kgw_reconcile_errors_total", `stage="apply"`)
	require.True(t, ok)
	assert.InDelta(t, 3.0, v, 0)
	v, ok = snap.value("go_goroutines", "")
	require.True(t, ok)
	assert.InDelta(t, 41.0, v, 0)
	_, ok = snap.sum("trickster_config_reload_failures_total")
	require.False(t, ok, "a counter the scrape does not expose is absent, not zero")
	_, ok = snap.value("go_goroutines", `x="y"`)
	require.False(t, ok)

	srv.Close()
	_, err = scrapeMetrics(addr)
	require.Error(t, err, "a failed scrape is an error rather than a snapshot of zeros")
}
