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
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const respHdrMatrixTmpl = `{"status":"success","data":{"resultType":"matrix","result":[` +
	`{"metric":{"job":"prometheus"},"values":[%s]}` +
	`]}}`

func mkRespHdrMatrix(start, end, step int64, val string) string {
	var b strings.Builder
	first := true
	for ts := start; ts <= end; ts += step {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, `[%d,%q]`, ts, val)
	}
	return fmt.Sprintf(respHdrMatrixTmpl, b.String())
}

// HX2: TSM merge picks one winner via headers.Merge, but Set-Cookie is
// multi-valued per RFC 6265. Cookies set by non-winning members are lost.
func TestALBResponseHeadersTSMSetCookie(t *testing.T) {
	mkOrigin := func(val, cookie string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/status/buildinfo" {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
				return
			}
			_ = r.ParseForm()
			start, _ := parseInt(r.Form.Get("start"))
			end, _ := parseInt(r.Form.Get("end"))
			step, _ := parseInt(r.Form.Get("step"))
			if step == 0 {
				step = 15
			}
			w.Header().Add("Set-Cookie", cookie)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, mkRespHdrMatrix(start, end, step, val))
		}))
	}

	upA := mkOrigin("1", "a=1; Path=/")
	upB := mkOrigin("2", "b=2; Path=/")
	t.Cleanup(upA.Close)
	t.Cleanup(upB.Close)

	frontPort := 19110
	metricsPort := 19111
	mgmtPort := 19112

	yaml := fmt.Sprintf(`
frontend:
  listen_port: %d
metrics:
  listen_port: %d
mgmt:
  listen_port: %d
logging:
  log_level: error
caches:
  mem1:
    provider: memory
backends:
  prom-a:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  prom-b:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  alb-tsm-cookies:
    provider: alb
    alb:
      mechanism: tsm
      output_format: prometheus
      pool:
        - prom-a
        - prom-b
`, frontPort, metricsPort, mgmtPort, upA.URL, upB.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	now := time.Now()
	end := now.Truncate(15 * time.Second)
	start := end.Add(-2 * time.Minute)
	params := url.Values{
		"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
		"start": {fmt.Sprintf("%d", start.Unix())},
		"end":   {fmt.Sprintf("%d", end.Unix())},
		"step":  {"15"},
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-cookies/api/v1/query_range?%s",
		frontPort, params.Encode())

	// retry until both backends are healthy and the merged response carries
	// both members' Set-Cookie values; a 200 alone can be served by a single
	// live member while the other healthcheck is still warming up
	var cookies []string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Get(u)
		if !assert.NoError(c, err) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equal(c, http.StatusOK, resp.StatusCode) {
			return
		}
		got := resp.Header.Values("Set-Cookie")
		hasA, hasB := false, false
		for _, v := range got {
			if strings.HasPrefix(v, "a=1") {
				hasA = true
			}
			if strings.HasPrefix(v, "b=2") {
				hasB = true
			}
		}
		if assert.Truef(c, hasA && hasB,
			"both members' Set-Cookie values should survive TSM merge; got %v", got) {
			cookies = got
		}
	}, 10*time.Second, 200*time.Millisecond, "alb-tsm-cookies never returned both Set-Cookie values")

	t.Logf("Set-Cookie values observed in merged TSM response: %v", cookies)
}

// HX3: TSM headers.Merge propagates the winner's Content-Encoding but mrf merges raw bytes -- mismatch if the winner was gzipped.
func TestALBResponseHeadersTSMContentEncoding(t *testing.T) {
	gzipBody := func(s string) []byte {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write([]byte(s))
		_ = gw.Close()
		return buf.Bytes()
	}

	mkGzipOrigin := func(val string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/status/buildinfo" {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
				return
			}
			_ = r.ParseForm()
			start, _ := parseInt(r.Form.Get("start"))
			end, _ := parseInt(r.Form.Get("end"))
			step, _ := parseInt(r.Form.Get("step"))
			if step == 0 {
				step = 15
			}
			body := gzipBody(mkRespHdrMatrix(start, end, step, val))
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		}))
	}
	mkPlainOrigin := func(val string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/status/buildinfo" {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
				return
			}
			_ = r.ParseForm()
			start, _ := parseInt(r.Form.Get("start"))
			end, _ := parseInt(r.Form.Get("end"))
			step, _ := parseInt(r.Form.Get("step"))
			if step == 0 {
				step = 15
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, mkRespHdrMatrix(start, end, step, val))
		}))
	}

	upA := mkGzipOrigin("1")
	upB := mkPlainOrigin("2")
	t.Cleanup(upA.Close)
	t.Cleanup(upB.Close)

	frontPort := 19120
	metricsPort := 19121
	mgmtPort := 19122

	yaml := fmt.Sprintf(`
frontend:
  listen_port: %d
metrics:
  listen_port: %d
mgmt:
  listen_port: %d
logging:
  log_level: error
caches:
  mem1:
    provider: memory
backends:
  prom-a:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  prom-b:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  alb-tsm-encoding:
    provider: alb
    alb:
      mechanism: tsm
      output_format: prometheus
      pool:
        - prom-a
        - prom-b
`, frontPort, metricsPort, mgmtPort, upA.URL, upB.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	now := time.Now()
	end := now.Truncate(15 * time.Second)
	start := end.Add(-2 * time.Minute)
	params := url.Values{
		"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
		"start": {fmt.Sprintf("%d", start.Unix())},
		"end":   {fmt.Sprintf("%d", end.Unix())},
		"step":  {"15"},
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-encoding/api/v1/query_range?%s",
		frontPort, params.Encode())

	var (
		ce   string
		body []byte
	)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Get(u)
		if !assert.NoError(c, err) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equal(c, http.StatusOK, resp.StatusCode) {
			return
		}
		ce = resp.Header.Get("Content-Encoding")
		body, err = io.ReadAll(resp.Body)
		assert.NoError(c, err)
	}, 10*time.Second, 200*time.Millisecond, "alb-tsm-encoding never returned 200")

	t.Logf("outbound Content-Encoding=%q body[:min(64,len)]=%q", ce, body[:min(64, len(body))])

	if ce == "" {
		return
	}
	if strings.EqualFold(ce, "gzip") {
		_, err := gzip.NewReader(bytes.NewReader(body))
		assert.NoErrorf(t, err,
			"outbound Content-Encoding=gzip but body is not valid gzip; merged bytes leaked under winner's encoding header. body[:64]=%q", body[:min(64, len(body))])
		return
	}
	t.Errorf("unexpected outbound Content-Encoding=%q on merged TSM response", ce)
}
