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
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tricksterHarness bundles the config path + listener addresses for a
// Trickster instance under test. Each test function constructs one (via a
// preset constructor or by hand for custom configs) and calls start.
type tricksterHarness struct {
	ConfigPath  string // path to YAML config passed to the daemon
	BaseAddr    string // host:port of the data listener (e.g. "127.0.0.1:8480")
	MetricsAddr string // host:port of the metrics/health listener
}

// developerHarness returns the harness for the shared developer config.
// Used by any test that just needs the full multi-backend playground
// (prom1/2/3, click1, flux2, rpc1, sim1, ...).
func developerHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "../docs/developer/environment/trickster-config/trickster.yaml",
		BaseAddr:    "127.0.0.1:8480",
		MetricsAddr: "127.0.0.1:8481",
	}
}

// albHarness returns the harness for the ALB-specific config (testdata/alb.yaml).
func albHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "testdata/alb.yaml",
		BaseAddr:    "127.0.0.1:8490",
		MetricsAddr: "127.0.0.1:8491",
	}
}

// rewriterHarness returns the harness for the request-rewriter config.
func rewriterHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "testdata/rewriter.yaml",
		BaseAddr:    "127.0.0.1:8493",
		MetricsAddr: "127.0.0.1:8494",
	}
}

// start boots a Trickster instance and blocks until the metrics listener is
// ready. Cancellation is bound to t.Cleanup, so callers do not need to manage
// the context themselves.
func (h tricksterHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", h.ConfigPath)
	waitForTrickster(t, h.MetricsAddr)
}

// requestOptions collects overrides for tricksterHarness.do.
type requestOptions struct {
	method      string
	headers     http.Header
	contentType string
	body        io.Reader
	params      url.Values
}

type requestOption func(*requestOptions)

// withMethod overrides the HTTP method (default GET, or POST if withBody is set).
func withMethod(m string) requestOption { return func(o *requestOptions) { o.method = m } }

// withHeader adds a single request header.
func withHeader(k, v string) requestOption {
	return func(o *requestOptions) {
		if o.headers == nil {
			o.headers = http.Header{}
		}
		o.headers.Add(k, v)
	}
}

// withBody sets a request body and Content-Type, and defaults the method to POST.
func withBody(contentType string, r io.Reader) requestOption {
	return func(o *requestOptions) {
		o.contentType = contentType
		o.body = r
		if o.method == "" {
			o.method = "POST"
		}
	}
}

// withParams sets URL query parameters.
func withParams(p url.Values) requestOption { return func(o *requestOptions) { o.params = p } }

// do issues a request to h.BaseAddr+path and returns the response (for
// headers/status) plus the decoded body. gzip Content-Encoding is handled
// transparently; other encodings are returned as-is so tests can assert on them.
func (h tricksterHarness) do(t *testing.T, path string, opts ...requestOption) (*http.Response, []byte) {
	t.Helper()
	o := &requestOptions{method: http.MethodGet}
	for _, opt := range opts {
		opt(o)
	}
	u := "http://" + h.BaseAddr + path
	if len(o.params) > 0 {
		u += "?" + o.params.Encode()
	}
	req, err := http.NewRequest(o.method, u, o.body)
	require.NoError(t, err)
	if o.contentType != "" {
		req.Header.Set("Content-Type", o.contentType)
	}
	for k, vs := range o.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// Disable automatic gzip so we control decoding — ALB mechanisms can
	// produce merged headers that confuse Go's auto-decompression.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		require.NoError(t, err)
		defer gr.Close()
		reader = gr
	}
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	return resp, body
}

// queryProm issues a request to a Prometheus-shaped backend and decodes the
// envelope into promResponse. Asserts 200 OK.
func (h tricksterHarness) queryProm(t *testing.T, backend, apiPath string, opts ...requestOption) (promResponse, http.Header) {
	t.Helper()
	resp, body := h.do(t, "/"+backend+apiPath, opts...)
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status %d: %s", resp.StatusCode, string(body))
	var pr promResponse
	require.NoError(t, json.Unmarshal(body, &pr))
	return pr, resp.Header.Clone()
}

// requireTricksterResult parses the X-Trickster-Result header and asserts
// that every key in want matches. Missing keys in the actual header fail the
// assertion. Extra keys in the actual header are ignored.
//
// Example:
//
//	requireTricksterResult(t, hdr, map[string]string{
//	    "engine": "DeltaProxyCache",
//	    "status": "kmiss",
//	})
func requireTricksterResult(t *testing.T, hdr http.Header, want map[string]string) {
	t.Helper()
	raw := hdr.Get("X-Trickster-Result")
	got := parseTricksterResult(raw)
	for k, v := range want {
		require.Equal(t, v, got[k], "X-Trickster-Result[%q] mismatch in %q", k, raw)
	}
}

// cacheProviderCase identifies a Prometheus backend wired to a specific cache
// provider in the developer config.
type cacheProviderCase struct {
	Name    string // subtest name, e.g. "memory"
	Backend string // backend id, e.g. "prom1"
}

// writeTestConfig clones the developer config with the frontend and metrics
// listen_port values replaced by the given ports. This gives each top-level
// test its own port range while preserving the full config (backends, caches,
// healthcheck settings, negative caching, etc.), eliminating port-release
// races between sequential tests that previously shared :8480/:8481.
func writeTestConfig(t *testing.T, frontPort, metricsPort, mgmtPort int) string {
	t.Helper()
	b, err := os.ReadFile("../docs/developer/environment/trickster-config/trickster.yaml")
	require.NoError(t, err)
	cfg := string(b)
	cfg = strings.Replace(cfg, "listen_port: 8480", fmt.Sprintf("listen_port: %d", frontPort), 1)
	cfg = strings.Replace(cfg, "listen_port: 8481", fmt.Sprintf("listen_port: %d", metricsPort), 1)
	// The dev config has no explicit mgmt section — inject one after metrics
	// so the mgmt listener doesn't collide across tests on the default port.
	cfg = strings.Replace(cfg, "listen_port: "+fmt.Sprintf("%d", metricsPort)+"\n",
		fmt.Sprintf("listen_port: %d\nmgmt:\n  listen_port: %d\n", metricsPort, mgmtPort), 1)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0644))
	return path
}

// defaultCacheProviders returns the cache-provider matrix exercised by the
// developer config: memory, filesystem, redis. bbolt and badger are covered
// by unit tests only.
func defaultCacheProviders() []cacheProviderCase {
	return []cacheProviderCase{
		{Name: "memory", Backend: "prom1"},
		{Name: "filesystem", Backend: "prom2"},
		{Name: "redis", Backend: "prom3"},
	}
}

// runCacheProviderMatrix runs fn as a subtest for each cache provider case.
// Use this instead of hand-rolling a for loop so future providers only need
// to be added to defaultCacheProviders.
func runCacheProviderMatrix(t *testing.T, fn func(t *testing.T, c cacheProviderCase)) {
	t.Helper()
	for _, c := range defaultCacheProviders() {
		t.Run(c.Name, func(t *testing.T) { fn(t, c) })
	}
}
