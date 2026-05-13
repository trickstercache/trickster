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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestALBTSMCorrectness(t *testing.T) {
	t.Run("D1 single member skips label stripping", func(t *testing.T) {
		const vectorBody = `{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"__name__":"up","job":"prometheus"},"value":[1700000000,"1"]},` +
			`{"metric":{"__name__":"up","job":"node"},"value":[1700000000,"1"]}` +
			`]}}`
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/status/buildinfo":
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
			default:
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, vectorBody)
			}
		}))
		t.Cleanup(mock.Close)

		frontPort := 18800
		metricsPort := 18801
		mgmtPort := 18802

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
  prom-labeled:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
    prometheus:
      labels:
        region: us-east
    healthcheck:
      path: /api/v1/status/buildinfo
      query: ""
      interval: 100ms
      timeout: 500ms
      failure_threshold: 1
      recovery_threshold: 1
  alb-tsm-single:
    provider: alb
    alb:
      mechanism: tsm
      output_format: prometheus
      pool:
        - prom-labeled
`, frontPort, metricsPort, mgmtPort, mock.URL)

		cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
		waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

		q := fmt.Sprintf("sum by (job) (up + 0*%d)", time.Now().UnixNano())
		u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-single/api/v1/query?query=%s",
			frontPort, url.QueryEscape(q))
		var body []byte
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			r, err := http.Get(u)
			if !assert.NoError(c, err) {
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if !assert.Equal(c, http.StatusOK, r.StatusCode, "body=%s", string(b)) {
				return
			}
			body = b
		}, 5*time.Second, 100*time.Millisecond, "alb pool never produced 200")

		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []struct {
			Metric map[string]string `json:"metric"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series, "expected at least one series; body=%s", string(body))
		for _, s := range series {
			require.Empty(t, s.Metric["region"],
				"single-member tsm pool must still strip injected labels for aggregation; got region=%q in metric=%v",
				s.Metric["region"], s.Metric)
		}
	})

	t.Run("D2 weighted avg silent failure when count fanout 500s", func(t *testing.T) {
		// Both upstreams answer the sum() rewrite with valid range data and
		// 500 on the count() rewrite. With FinalizeWeightedAvg gated on
		// (sumTS != nil && countTS != nil), the response degrades to the
		// raw sum without any warning.
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

		var sumHits, countHits, otherHits atomic.Int64
		makeMock := func(sumVal string) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/status/buildinfo" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
					return
				}
				_ = r.ParseForm()
				q := r.Form.Get("query")
				switch {
				case strings.HasPrefix(q, "count("):
					countHits.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, `{"status":"error","errorType":"internal","error":"count failed"}`)
				case strings.HasPrefix(q, "sum("):
					sumHits.Add(1)
					start, _ := parseInt(r.Form.Get("start"))
					end, _ := parseInt(r.Form.Get("end"))
					step, _ := parseInt(r.Form.Get("step"))
					if step == 0 {
						step = 15
					}
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, mkMatrix(start, end, step, sumVal))
				default:
					otherHits.Add(1)
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[`+
						`{"metric":{"__name__":"up","job":"prometheus"},"value":[1700000000,"1"]}]}}`)
				}
			}))
		}

		m1 := makeMock("10")
		t.Cleanup(m1.Close)
		m2 := makeMock("20")
		t.Cleanup(m2.Close)

		frontPort := 18810
		metricsPort := 18811
		mgmtPort := 18812

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
  alb-tsm-avg:
    provider: alb
    alb:
      mechanism: tsm
      output_format: prometheus
      pool:
        - prom-a
        - prom-b
`, frontPort, metricsPort, mgmtPort, m1.URL, m2.URL)

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
			"query": {fmt.Sprintf("avg(up + 0*%d)", now.UnixNano())},
			"start": {fmt.Sprintf("%d", start.Unix())},
			"end":   {fmt.Sprintf("%d", end.Unix())},
			"step":  {"15"},
		}
		u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-avg/api/v1/query_range?%s",
			frontPort, params.Encode())
		var resp *http.Response
		var body []byte
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			r, err := http.Get(u)
			if !assert.NoError(c, err) {
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if !assert.Greater(c, sumHits.Load(), int64(0), "waiting for pool to fan out sum()") {
				return
			}
			resp, body = r, b
		}, 5*time.Second, 100*time.Millisecond, "alb pool never fanned out the sum query")

		require.Greater(t, sumHits.Load(), int64(0), "expected sum() rewrite to reach upstreams")
		require.Greater(t, countHits.Load(), int64(0), "expected count() rewrite to reach upstreams")

		t.Logf("status=%d sumHits=%d countHits=%d otherHits=%d body=%s",
			resp.StatusCode, sumHits.Load(), countHits.Load(), otherHits.Load(), string(body))

		var pr promResponse
		if err := json.Unmarshal(body, &pr); err == nil {
			var qd promQueryData
			_ = json.Unmarshal(pr.Data, &qd)
			var series []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			}
			_ = json.Unmarshal(qd.Result, &series)
			t.Logf("parsed status=%q resultType=%q series=%d", pr.Status, qd.ResultType, len(series))
			for i, s := range series {
				if len(s.Values) > 0 {
					t.Logf("series[%d] metric=%v first=%v last=%v",
						i, s.Metric, s.Values[0], s.Values[len(s.Values)-1])
				}
			}
		}

		nonOK := resp.StatusCode >= 400
		hasWarn := strings.Contains(string(body), `"warnings"`)
		result := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		hasPhit := strings.Contains(result["status"], "phit")
		assert.Truef(t, nonOK || hasWarn || hasPhit,
			"avg fanout silently returned 200 with raw sum: status=%d X-Trickster-Result=%q body=%s",
			resp.StatusCode, resp.Header.Get("X-Trickster-Result"), string(body))
	})

	t.Run("V2 partial failure surfaces in X-Trickster-Result", func(t *testing.T) {
		const okVector = `{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"__name__":"up","job":"prometheus"},"value":[1700000000,"1"]}]}}`

		makeOK := func() *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/status/buildinfo" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
					return
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, okVector)
			}))
		}
		makeBroken := func() *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/status/buildinfo" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"status":"error","errorType":"internal","error":"upstream down"}`)
			}))
		}

		ok := makeOK()
		t.Cleanup(ok.Close)
		b1 := makeBroken()
		t.Cleanup(b1.Close)
		b2 := makeBroken()
		t.Cleanup(b2.Close)
		b3 := makeBroken()
		t.Cleanup(b3.Close)

		frontPort := 18820
		metricsPort := 18821
		mgmtPort := 18822

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
  prom-ok:
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
  prom-b1:
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
  prom-b2:
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
  prom-b3:
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
  alb-tsm-partial:
    provider: alb
    alb:
      mechanism: tsm
      output_format: prometheus
      pool:
        - prom-ok
        - prom-b1
        - prom-b2
        - prom-b3
`, frontPort, metricsPort, mgmtPort, ok.URL, b1.URL, b2.URL, b3.URL)

		cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
		waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

		// Poll until mixed 2xx+5xx fanout occurs; healthcheck registration is async.
		var (
			resp *http.Response
			body []byte
			raw  string
		)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano())
			u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-partial/api/v1/query?query=%s",
				frontPort, url.QueryEscape(q))
			r, err := http.Get(u)
			if !assert.NoError(c, err) {
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			rh := r.Header.Get("X-Trickster-Result")
			if !assert.Contains(c, rh, "phit", "X-Trickster-Result=%q body=%s", rh, string(b)) {
				return
			}
			resp, body, raw = r, b, rh
		}, 5*time.Second, 100*time.Millisecond,
			"alb pool never produced a mixed 2xx+5xx fanout")

		t.Logf("status=%d X-Trickster-Result=%q body=%s",
			resp.StatusCode, raw, string(body))

		require.Equal(t, http.StatusOK, resp.StatusCode,
			"current behavior is 200 (lowest non-zero status wins); body=%s", string(body))

		require.Contains(t, raw, "phit",
			"3 of 4 fanout members 500'd; expected partial-hit marker in X-Trickster-Result, got %q", raw)
	})
}

func parseInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
