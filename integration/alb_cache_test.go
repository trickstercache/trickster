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
	"github.com/trickstercache/trickster/v2/integration/promstub"
)

func TestALBCache(t *testing.T) {
	t.Run("C1 shared cache_key_prefix collides across pool members", func(t *testing.T) {
		respTmpl := albTestdata(t, "alb_cache/c1_vector.json.tmpl")

		var aHits, bHits atomic.Int64
		mk := func(label, val string, hits *atomic.Int64) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == promstub.BuildInfoPath {
					promstub.WriteBuildInfo(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, respTmpl, label, val)
			}))
		}
		upstreamA := mk("a", "1", &aHits)
		t.Cleanup(upstreamA.Close)
		upstreamB := mk("b", "2", &bHits)
		t.Cleanup(upstreamB.Close)

		frontPort := 18900
		metricsPort := 18901
		mgmtPort := 18902

		yaml := fmt.Sprintf(albTestdata(t, "alb_cache/c1.yaml.tmpl"),
			frontPort, metricsPort, mgmtPort, upstreamA.URL, upstreamB.URL)

		cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
		waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

		// Use a query unique to this run so prior cache state doesn't taint.
		q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano())
		u := fmt.Sprintf("http://127.0.0.1:%d/alb-shared-cache/api/v1/query?query=%s",
			frontPort, url.QueryEscape(q))

		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		seenJobs := make(map[string]int)
		seenValues := make(map[string]int)
		// Wait until both pool members have been queried at least once,
		// otherwise RR can be sticky to whichever member became healthy first
		// and the assertion below would fail on healthcheck timing rather
		// than the cache-key collision being tested.
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			r, err := client.Get(u)
			if !assert.NoError(c, err) {
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			assert.Equal(c, http.StatusOK, r.StatusCode, "body=%s", string(b))
			assert.GreaterOrEqual(c, aHits.Load(), int64(1), "waiting for upstream A to be queried")
			assert.GreaterOrEqual(c, bHits.Load(), int64(1), "waiting for upstream B to be queried")
		}, 10*time.Second, 100*time.Millisecond, "alb pool never queried both members")

		const reqs = 8
		for range reqs {
			resp, err := client.Get(u)
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))
			var pr promResponse
			require.NoError(t, json.Unmarshal(body, &pr))
			var qd promQueryData
			require.NoError(t, json.Unmarshal(pr.Data, &qd))
			var series []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			}
			require.NoError(t, json.Unmarshal(qd.Result, &series))
			require.NotEmpty(t, series)
			for _, s := range series {
				seenJobs[s.Metric["job"]]++
				if len(s.Value) >= 2 {
					if v, ok := s.Value[1].(string); ok {
						seenValues[v]++
					}
				}
			}
		}

		t.Logf("aHits=%d bHits=%d seenJobs=%v seenValues=%v",
			aHits.Load(), bHits.Load(), seenJobs, seenValues)

		assert.Greater(t, seenJobs["a"], 0,
			"upstream A's job label never surfaced; cache likely pinned all reads to upstream B")
		assert.Greater(t, seenJobs["b"], 0,
			"upstream B's job label never surfaced; cache likely pinned all reads to upstream A")
		assert.GreaterOrEqual(t, len(seenValues), 2,
			"expected both values 1 and 2 in responses across %d round-robin requests; got values=%v",
			reqs, seenValues)
	})

	t.Run("C2 tsm tolerates one member with unsupported content-encoding", func(t *testing.T) {
		promRange := albTestdata(t, "alb_cache/c2_prom_range.json.tmpl")

		mkRange := func(start, end, step int64) string {
			var b strings.Builder
			first := true
			for ts := start; ts <= end; ts += step {
				if !first {
					b.WriteString(",")
				}
				first = false
				fmt.Fprintf(&b, `[%d,"1"]`, ts)
			}
			return fmt.Sprintf(promRange, b.String())
		}

		makeOK := func() *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == promstub.BuildInfoPath {
					promstub.WriteBuildInfo(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = r.ParseForm()
				start, _ := parseInt(r.Form.Get("start"))
				end, _ := parseInt(r.Form.Get("end"))
				step, _ := parseInt(r.Form.Get("step"))
				if step == 0 {
					step = 15
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, mkRange(start, end, step))
			}))
		}
		var m2QueryHits atomic.Int64
		makeBadEncoding := func(qhits *atomic.Int64) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == promstub.BuildInfoPath {
					promstub.WriteBuildInfo(w)
					return
				}
				qhits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", "xyz")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("not actually xyz-encoded"))
			}))
		}

		m0 := makeOK()
		t.Cleanup(m0.Close)
		m1 := makeOK()
		t.Cleanup(m1.Close)
		m2 := makeBadEncoding(&m2QueryHits)
		t.Cleanup(m2.Close)

		frontPort := 18910
		metricsPort := 18911
		mgmtPort := 18912

		yaml := fmt.Sprintf(albTestdata(t, "alb_cache/c2.yaml.tmpl"),
			frontPort, metricsPort, mgmtPort, m0.URL, m1.URL, m2.URL)

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

		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		var resp *http.Response
		var body []byte
		// Poll until the bad-encoding member has been queried at least once,
		// otherwise the test's race window can leave m2 unhealthy and never
		// exercised, making the partial-failure surface untested.
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			r, err := client.Get(u)
			if !assert.NoError(c, err) {
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			resp, body = r, b
			assert.NotEmpty(c, b, "waiting for non-empty merged body")
			assert.GreaterOrEqual(c, m2QueryHits.Load(), int64(1),
				"waiting for bad-encoding member to be queried")
		}, 5*time.Second, 100*time.Millisecond, "alb pool never queried the bad-encoding member")

		t.Logf("status=%d X-Trickster-Result=%q body=%s",
			resp.StatusCode, resp.Header.Get("X-Trickster-Result"), string(body))

		require.GreaterOrEqual(t, resp.StatusCode, 200,
			"expected a response, got status=%d body=%s", resp.StatusCode, string(body))
		require.Less(t, resp.StatusCode, 500,
			"one bad-encoding member should not 5xx the merged response; status=%d body=%s",
			resp.StatusCode, string(body))

		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr),
			"merged body must be valid prom JSON; status=%d body=%s", resp.StatusCode, string(body))
		require.Equal(t, "success", pr.Status,
			"merged body should report success when 2 of 3 members are healthy; body=%s", string(body))

		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series,
			"expected merged series from members 0+1; body=%s", string(body))

		raw := resp.Header.Get("X-Trickster-Result")
		hasPhit := strings.Contains(raw, "phit")
		hasWarn := strings.Contains(string(body), `"warnings"`) ||
			strings.Contains(string(body), "encoding") ||
			strings.Contains(string(body), "unsupported")
		assert.Truef(t, hasPhit || hasWarn,
			"expected partial-hit marker or encoding warning surfaced to client; X-Trickster-Result=%q body=%s",
			raw, string(body))
	})

	t.Run("V3 tsm phit marker for mixed cache hit/miss across members", func(t *testing.T) {
		matrixTmpl := albTestdata(t, "alb_cache/v3_matrix.json.tmpl")
		var m1Hits, m2Hits atomic.Int64
		mk := func(hits *atomic.Int64) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == promstub.BuildInfoPath {
					promstub.WriteBuildInfo(w)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = r.ParseForm()
				start, _ := parseInt(r.Form.Get("start"))
				end, _ := parseInt(r.Form.Get("end"))
				step, _ := parseInt(r.Form.Get("step"))
				if step == 0 {
					step = 15
				}
				hits.Add(1)
				var b strings.Builder
				first := true
				for ts := start; ts <= end; ts += step {
					if !first {
						b.WriteString(",")
					}
					first = false
					fmt.Fprintf(&b, `[%d,"1"]`, ts)
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, matrixTmpl, b.String())
			}))
		}
		up1 := mk(&m1Hits)
		t.Cleanup(up1.Close)
		up2 := mk(&m2Hits)
		t.Cleanup(up2.Close)

		frontPort := 18920
		metricsPort := 18921
		mgmtPort := 18922

		yaml := fmt.Sprintf(albTestdata(t, "alb_cache/v3.yaml.tmpl"),
			frontPort, metricsPort, mgmtPort, up1.URL, up2.URL)

		cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
		waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		now := time.Now()
		end := now.Truncate(15 * time.Second)
		start := end.Add(-2 * time.Minute)
		params := url.Values{
			"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
			"start": {fmt.Sprintf("%d", start.Unix())},
			"end":   {fmt.Sprintf("%d", end.Unix())},
			"step":  {"15"},
		}
		u := fmt.Sprintf("http://127.0.0.1:%d/alb-tsm-mixed-cache/api/v1/query_range?%s",
			frontPort, params.Encode())

		// Warmup until both members are queried; else TSM may ship to one and leave the miss-state ambiguous.
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			r, err := client.Get(u)
			if !assert.NoError(c, err) {
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			assert.Equal(c, http.StatusOK, r.StatusCode, "body=%s", string(b))
			assert.GreaterOrEqual(c, m1Hits.Load(), int64(1),
				"warmup waiting for prom-c1 to receive a request")
			assert.GreaterOrEqual(c, m2Hits.Load(), int64(1),
				"warmup waiting for prom-c2 to receive a request")
		}, 10*time.Second, 250*time.Millisecond, "warmup never reached both members")

		bypass := fmt.Sprintf("http://127.0.0.1:%d/prom-c2/api/v1/query_range?%s",
			frontPort, params.Encode())
		req, _ := http.NewRequest("GET", bypass, nil)
		req.Header.Set("Cache-Control", "no-cache")
		r2, err := client.Do(req)
		require.NoError(t, err)
		_, _ = io.ReadAll(r2.Body)
		r2.Body.Close()

		resp, err := client.Get(u)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		raw := resp.Header.Get("X-Trickster-Result")
		t.Logf("status=%d X-Trickster-Result=%q m1Hits=%d m2Hits=%d body=%s",
			resp.StatusCode, raw, m1Hits.Load(), m2Hits.Load(), string(body))

		require.Equal(t, http.StatusOK, resp.StatusCode)

		assert.Containsf(t, raw, "phit",
			"mixed cache hit/miss across pool members did not surface phit in X-Trickster-Result=%q",
			raw)
	})
}
