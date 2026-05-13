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
	"context"
	"fmt"
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

func TestALBPerPathHeadersTSM(t *testing.T) {
	const matrixBodyTmpl = `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"job":"prometheus"},"values":[%s]}` +
		`]}}`

	mkMatrix := func(start, end, step int64, val string) string {
		var b strings.Builder
		first := true
		for ts := start; ts <= end; ts += step {
			if !first {
				b.WriteString(",")
			}
			first = false
			fmt.Fprintf(&b, `[%d,%q]`, ts, val)
		}
		return fmt.Sprintf(matrixBodyTmpl, b.String())
	}

	mkUpstream := func(val string) *httptest.Server {
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
			fmt.Fprint(w, mkMatrix(start, end, step, val))
		}))
	}

	upA := mkUpstream("1")
	upB := mkUpstream("2")
	t.Cleanup(upA.Close)
	t.Cleanup(upB.Close)

	frontPort := 19000
	metricsPort := 19001
	mgmtPort := 19002

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
    paths:
      - path: /api/v1/query_range
        handler: query_range
        methods: [GET, POST]
        match_type: exact
        cache_key_params: [query, start, end, step]
        response_headers:
          X-Origin: A
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
    paths:
      - path: /api/v1/query_range
        handler: query_range
        methods: [GET, POST]
        match_type: exact
        cache_key_params: [query, start, end, step]
        response_headers:
          X-Origin: B
  alb-tsm-perpath:
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
	u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-perpath/api/v1/query_range?%s",
		frontPort, params.Encode())

	var hdr http.Header
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		resp, err := http.Get(u)
		if !assert.NoError(c, err) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equal(c, http.StatusOK, resp.StatusCode) {
			return
		}
		hdr = resp.Header.Clone()
	}, 10*time.Second, 200*time.Millisecond, "alb-tsm-perpath never returned 200")

	xo := hdr.Values("X-Origin")
	t.Logf("X-Origin values observed in merged TSM response: %v", xo)
	assert.NotEmptyf(t, xo,
		"expected per-path X-Origin header to survive TSM merge; full headers=%v", hdr)
}
