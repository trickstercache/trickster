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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"gopkg.in/yaml.v2"
)

type tricksterHarness struct {
	ConfigPath  string // path to YAML config passed to the daemon
	BaseAddr    string // host:port of the data listener (e.g. "127.0.0.1:8480")
	MetricsAddr string // host:port of the metrics/health listener
}

func developerHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "../docs/developer/environment/trickster-config/trickster.yaml",
		BaseAddr:    "127.0.0.1:8480",
		MetricsAddr: "127.0.0.1:8481",
	}
}

func albHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "testdata/alb.yaml",
		BaseAddr:    "127.0.0.1:8490",
		MetricsAddr: "127.0.0.1:8491",
	}
}

func rewriterHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "testdata/rewriter.yaml",
		BaseAddr:    "127.0.0.1:8493",
		MetricsAddr: "127.0.0.1:8494",
	}
}

func (h tricksterHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", h.ConfigPath)
	waitForTrickster(t, h.MetricsAddr)
}

type requestOptions struct {
	method      string
	headers     http.Header
	contentType string
	body        io.Reader
	params      url.Values
}

type requestOption func(*requestOptions)

func withMethod(m string) requestOption { return func(o *requestOptions) { o.method = m } }

func withHeader(k, v string) requestOption {
	return func(o *requestOptions) {
		if o.headers == nil {
			o.headers = http.Header{}
		}
		o.headers.Add(k, v)
	}
}

func withBody(contentType string, r io.Reader) requestOption {
	return func(o *requestOptions) {
		o.contentType = contentType
		o.body = r
		if o.method == "" {
			o.method = "POST"
		}
	}
}

func withParams(p url.Values) requestOption { return func(o *requestOptions) { o.params = p } }

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
	// ALB mechanisms can produce merged headers that confuse Go's auto-decompression.
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

func (h tricksterHarness) queryProm(t *testing.T, backend, apiPath string, opts ...requestOption) (promResponse, http.Header) {
	t.Helper()
	resp, body := h.do(t, "/"+backend+apiPath, opts...)
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status %d: %s", resp.StatusCode, string(body))
	var pr promResponse
	require.NoError(t, json.Unmarshal(body, &pr))
	return pr, resp.Header.Clone()
}

func requireTricksterResult(t *testing.T, hdr http.Header, want map[string]string) {
	t.Helper()
	raw := hdr.Get("X-Trickster-Result")
	got := parseTricksterResult(raw)
	for k, v := range want {
		require.Equal(t, v, got[k], "X-Trickster-Result[%q] mismatch in %q", k, raw)
	}
}

type cacheProviderCase struct {
	Name    string // subtest name, e.g. "memory"
	Backend string // backend id, e.g. "prom1"
}

func writeTestConfig(t *testing.T, frontPort, metricsPort, mgmtPort int) string {
	t.Helper()
	b, err := os.ReadFile("../docs/developer/environment/trickster-config/trickster.yaml")
	require.NoError(t, err)
	var c tkconfig.Config
	require.NoError(t, yaml.Unmarshal(b, &c))
	c.Frontend.ListenPort = frontPort
	c.Metrics.ListenPort = metricsPort
	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}
	c.MgmtConfig.ListenPort = mgmtPort
	out, err := yaml.Marshal(&c)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, out, 0644))
	return path
}

func defaultCacheProviders() []cacheProviderCase {
	return []cacheProviderCase{
		{Name: "memory", Backend: "prom1"},
		{Name: "filesystem", Backend: "prom2"},
		{Name: "redis", Backend: "prom3"},
	}
}

func runCacheProviderMatrix(t *testing.T, fn func(t *testing.T, c cacheProviderCase)) {
	t.Helper()
	for _, c := range defaultCacheProviders() {
		t.Run(c.Name, func(t *testing.T) { fn(t, c) })
	}
}
