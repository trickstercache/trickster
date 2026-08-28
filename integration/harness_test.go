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
	"testing"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type tricksterHarness struct {
	ConfigPath                 string // path to YAML config passed to the daemon
	BaseAddr                   string // host:port of the data listener (e.g. "127.0.0.1:8480")
	MetricsAddr                string // host:port of the metrics/health listener
	MgmtAddr                   string // host:port of the management listener
	MySQLAddr                  string // host:port of the MySQL protocol listener, when configured
	ClickHouseNativeAddr       string // native listener backed by ClickHouse HTTP
	ClickHouseNativeOriginAddr string // native listener backed by ClickHouse native
	releasePorts               func() // releases ports reserved while the config is prepared
}

func developerHarness(t *testing.T) tricksterHarness {
	t.Helper()
	return configHarness(t)
}

func albHarness(t *testing.T) tricksterHarness {
	t.Helper()
	return staticConfigHarness(t, "testdata/alb.yaml")
}

func rewriterHarness(t *testing.T) tricksterHarness {
	t.Helper()
	return staticConfigHarness(t, "testdata/rewriter.yaml")
}

func (h tricksterHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if h.releasePorts != nil {
		h.releasePorts()
	}
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

func configHarness(t *testing.T, mods ...func(*tkconfig.Config)) tricksterHarness {
	t.Helper()
	ports, release := portutil.Reserve(t, 6)
	frontPort, metricsPort, mgmtPort, mysqlPort := ports[0], ports[1], ports[2], ports[3]
	clickHouseHTTPOriginPort, clickHouseNativeOriginPort := ports[4], ports[5]
	return tricksterHarness{
		ConfigPath: writeTestConfig(t,
			"../docs/developer/environment/trickster-config/trickster.yaml",
			frontPort, metricsPort, mgmtPort, mysqlPort, clickHouseHTTPOriginPort, clickHouseNativeOriginPort, mods...),
		BaseAddr:                   fmt.Sprintf("127.0.0.1:%d", frontPort),
		MetricsAddr:                fmt.Sprintf("127.0.0.1:%d", metricsPort),
		MgmtAddr:                   fmt.Sprintf("127.0.0.1:%d", mgmtPort),
		MySQLAddr:                  fmt.Sprintf("127.0.0.1:%d", mysqlPort),
		ClickHouseNativeAddr:       fmt.Sprintf("127.0.0.1:%d", clickHouseHTTPOriginPort),
		ClickHouseNativeOriginAddr: fmt.Sprintf("127.0.0.1:%d", clickHouseNativeOriginPort),
		releasePorts:               release,
	}
}

func staticConfigHarness(t *testing.T, configPath string) tricksterHarness {
	t.Helper()
	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]
	return tricksterHarness{
		ConfigPath:   writeTestConfig(t, configPath, frontPort, metricsPort, mgmtPort, 0, 0, 0),
		BaseAddr:     fmt.Sprintf("127.0.0.1:%d", frontPort),
		MetricsAddr:  fmt.Sprintf("127.0.0.1:%d", metricsPort),
		MgmtAddr:     fmt.Sprintf("127.0.0.1:%d", mgmtPort),
		releasePorts: release,
	}
}

func writeTestConfig(t *testing.T, configPath string,
	frontPort, metricsPort, mgmtPort, mysqlPort, clickHouseHTTPOriginPort, clickHouseNativeOriginPort int,
	mods ...func(*tkconfig.Config),
) string {
	t.Helper()
	b, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var c tkconfig.Config
	require.NoError(t, yaml.Unmarshal(b, &c))
	if c.Listeners == nil {
		c.Listeners = make(listener.Lookup)
	}
	if _, ok := c.Listeners["default"]; !ok {
		if c.Frontend != nil {
			c.Listeners["default"] = listener.FromFrontend(c.Frontend)
		} else {
			c.Listeners["default"] = &listener.Options{ListenPort: 8480}
		}
	}
	if _, ok := c.Listeners["metrics"]; !ok {
		c.Listeners["metrics"] = &listener.Options{
			ListenPort: 8481,
		}
	}
	if _, ok := c.Listeners["mgmt"]; !ok {
		c.Listeners["mgmt"] = &listener.Options{
			ListenPort: 8484,
		}
	}
	c.Listeners["default"].ListenPort = frontPort
	c.Listeners["default"].ListenAddress = "127.0.0.1"
	c.Listeners["metrics"].ListenPort = metricsPort
	c.Listeners["metrics"].ListenAddress = "127.0.0.1"
	c.Listeners["mgmt"].ListenPort = mgmtPort
	c.Listeners["mgmt"].ListenAddress = "127.0.0.1"
	if mysqlListener := c.Listeners["mysql1"]; mysqlListener != nil {
		mysqlListener.ListenAddress = "0.0.0.0"
		mysqlListener.ListenPort = mysqlPort
	}
	if clickHouseHTTPOriginPort > 0 && c.Backends["click1"] != nil {
		c.Listeners["clickhouse-native"] = &listener.Options{
			Protocol: listener.ProtocolClickHouse, ListenAddress: "127.0.0.1", ListenPort: clickHouseHTTPOriginPort,
		}
		c.Backends["click1"].ListenerNames = []string{listener.DefaultFrontendName, "clickhouse-native"}
	}
	if clickHouseNativeOriginPort > 0 && c.Backends["click1"] != nil {
		c.Listeners["clickhouse-native-origin-native"] = &listener.Options{
			Protocol: listener.ProtocolClickHouse, ListenAddress: "127.0.0.1", ListenPort: clickHouseNativeOriginPort,
		}
		nativeOrigin := c.Backends["click1"].Clone()
		nativeOrigin.Name = "click-native"
		nativeOrigin.OriginURL = "http://127.0.0.1:9000"
		nativeOrigin.Protocol = "native"
		nativeOrigin.CacheName = "mem2"
		nativeOrigin.ListenerNames = []string{listener.DefaultFrontendName, "clickhouse-native-origin-native"}
		c.Backends["click-native"] = nativeOrigin
	}
	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}
	c.Frontend = nil
	c.Metrics = nil
	c.MgmtConfig.ListenAddress = ""
	c.MgmtConfig.ListenPort = 0
	for _, mod := range mods {
		mod(&c)
	}
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
