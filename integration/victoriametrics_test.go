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

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"

	"github.com/stretchr/testify/require"
)

const (
	vmOrigin  = "http://127.0.0.1:8428"
	vmBackend = "victoriametrics1"
	// a settled window of the seeded trips history, before live scraping began; 73 points
	vmStep = 300
	// VictoriaMetrics aggregates series in parallel, so a sum's last bit can differ between two
	// evaluations of the same query; samples are compared to this many significant digits
	vmSampleDigits = 12
)

// requireVictoriaMetrics skips when the developer environment runs without VictoriaMetrics, unless
// TRICKSTER_VICTORIAMETRICS_TEST requires it, as CI's integration profile does.
func requireVictoriaMetrics(t *testing.T) {
	t.Helper()
	resp, err := http.Get(vmOrigin + "/health")
	if err == nil {
		resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		if os.Getenv("TRICKSTER_VICTORIAMETRICS_TEST") == "1" {
			t.Fatalf("VictoriaMetrics is not healthy at %s: %v", vmOrigin, err)
		}
		t.Skip("VictoriaMetrics is not running; start the victoriametrics compose profile")
	}
}

type vmFetch struct {
	code   int
	result map[string]string
	body   []byte
}

func vmGet(t *testing.T, base, path string, v url.Values, method string, hdr http.Header) vmFetch {
	t.Helper()
	var body io.Reader
	target := base + path
	if method == http.MethodPost {
		body = strings.NewReader(v.Encode())
	} else if len(v) > 0 {
		target += "?" + v.Encode()
	}
	r, err := http.NewRequest(method, target, body)
	require.NoError(t, err)
	for k, vv := range hdr {
		r.Header[k] = vv
	}
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return vmFetch{resp.StatusCode, parseTricksterResult(resp.Header.Get("X-Trickster-Result")), b}
}

// vmNormalize drops the per-evaluation stats object, orders series and rounds samples, so a cached
// answer can be compared with the origin's.
func vmNormalize(t *testing.T, b []byte) any {
	t.Helper()
	var d any
	require.NoError(t, json.Unmarshal(b, &d), string(b))
	if m, ok := d.(map[string]any); ok {
		delete(m, "stats")
		if data, ok := m["data"].(map[string]any); ok {
			if res, ok := data["result"].([]any); ok {
				slices.SortFunc(res, func(a, b any) int {
					ja, _ := json.Marshal(a.(map[string]any)["metric"])
					jb, _ := json.Marshal(b.(map[string]any)["metric"])
					return strings.Compare(string(ja), string(jb))
				})
				for _, series := range res {
					sm, _ := series.(map[string]any)
					vmRoundSample(sm["value"])
					if values, ok := sm["values"].([]any); ok {
						for _, sample := range values {
							vmRoundSample(sample)
						}
					}
				}
			}
		}
	}
	return d
}

// rounds the value of a [timestamp, "value"] sample in place
func vmRoundSample(sample any) {
	pair, ok := sample.([]any)
	if !ok || len(pair) != 2 {
		return
	}
	if s, ok := pair[1].(string); ok {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			pair[1] = strconv.FormatFloat(f, 'g', vmSampleDigits, 64)
		}
	}
}

func TestVictoriaMetrics(t *testing.T) {
	requireVictoriaMetrics(t)
	h := developerHarness(t)
	h.start(t)
	proxy := "http://" + h.BaseAddr + "/" + vmBackend
	end := time.Now().Add(-3*time.Hour).Unix() / vmStep * vmStep
	start := end - 72*vmStep
	rng := func(query string, shift int64) url.Values {
		return url.Values{
			"query": {query}, "start": {strconv.FormatInt(start+shift, 10)},
			"end": {strconv.FormatInt(end+shift, 10)}, "step": {strconv.Itoa(vmStep)},
		}
	}
	// same answer as the origin, through the named cache path
	same := func(t *testing.T, path string, v url.Values, method string, hdr http.Header, engine string) vmFetch {
		t.Helper()
		got := vmGet(t, proxy, path, v, method, hdr)
		want := vmGet(t, vmOrigin, path, v, method, hdr)
		require.Equal(t, want.code, got.code, string(got.body))
		require.Equal(t, vmNormalize(t, want.body), vmNormalize(t, got.body), "%s %v", path, v)
		require.Equal(t, engine, got.result[keys.Engine], "X-Trickster-Result %v", got.result)
		return got
	}
	metrics := "http://" + h.MetricsAddr + "/metrics"
	deltaKey := metricsutil.Key("trickster_victoriametrics_query_analysis_total",
		map[string]string{"backend_name": vmBackend, "cache_mode": "delta", "reason": "eligible"})
	before := metricsutil.ScrapeURL(t, metrics, nil)[deltaKey]

	t.Run("delta cache", func(t *testing.T) {
		for _, q := range []string{
			"sum(trips_total) - sum(trips_total offset 5m)",
			`sum by (__name__) (increase({__name__=~"trips_(fares|tips)_dollars_total"}[1h]) keep_metric_names)`,
			"WITH (d(m) = sum(m) - sum(m offset 15m)) d(trips_total)",
		} {
			v := rng(q, 0)
			first := same(t, "/api/v1/query_range", v, http.MethodGet, nil, "DeltaProxyCache")
			require.Contains(t, []string{"kmiss", "hit", "phit"}, first.result[keys.Status])
			requireCacheHit(t, func() map[string]string {
				return same(t, "/api/v1/query_range", v, http.MethodGet, nil, "DeltaProxyCache").result
			}, q)
			// two steps later, only the new points are fetched
			shifted := same(t, "/api/v1/query_range", rng(q, 2*vmStep), http.MethodGet, nil, "DeltaProxyCache")
			require.Equal(t, "phit", shifted.result[keys.Status], q)
		}
		require.Greater(t, metricsutil.ScrapeURL(t, metrics, nil)[deltaKey], before)
	})

	t.Run("aligned like VictoriaMetrics", func(t *testing.T) {
		v := rng("sum(trips_in_progress)", 17)
		same(t, "/api/v1/query_range", v, http.MethodGet, nil, "DeltaProxyCache")
		same(t, "/api/v1/query_range", v, http.MethodPost, nil, "DeltaProxyCache")
	})

	t.Run("object and relayed shapes", func(t *testing.T) {
		v := rng("topk_avg(2, sum by (borough) (increase(trips_total[1h])))", 0)
		same(t, "/api/v1/query_range", v, http.MethodGet, nil, "ObjectProxyCache")
		requireCacheHit(t, func() map[string]string {
			return same(t, "/api/v1/query_range", v, http.MethodGet, nil, "ObjectProxyCache").result
		})
		v = rng("sum(trips_total)", 0)
		v.Set("nocache", "1")
		same(t, "/api/v1/query_range", v, http.MethodGet, nil, "HTTPProxy")
		bad := vmGet(t, proxy, "/api/v1/query_range", rng("sum(", 0), http.MethodGet, nil)
		require.Equal(t, http.StatusBadRequest, bad.code)
		require.Contains(t, string(bad.body), `"errorType":"400"`)
	})

	t.Run("instant and metadata", func(t *testing.T) {
		at := url.Values{"query": {"sum by (borough) (trips_total)"}, "time": {strconv.FormatInt(end, 10)}}
		same(t, "/api/v1/query", at, http.MethodGet, nil, "ObjectProxyCache")
		requireCacheHit(t, func() map[string]string {
			return same(t, "/api/v1/query", at, http.MethodGet, nil, "ObjectProxyCache").result
		})
		window := url.Values{"start": {strconv.FormatInt(start, 10)}, "end": {strconv.FormatInt(end, 10)}}
		same(t, "/api/v1/labels", window, http.MethodGet, nil, "ObjectProxyCache")
		window.Set("match[]", "trips_in_progress")
		same(t, "/api/v1/series", window, http.MethodGet, nil, "ObjectProxyCache")
	})

	t.Run("graphite", func(t *testing.T) {
		// the Graphite fixture holds 48 hours of 10s data
		gEnd := time.Now().Add(-2 * time.Hour).Unix()
		v := url.Values{
			"target": {"aliasByNode(vmgraphite.fast.*.*.requests, 2, 3)"},
			"from":   {strconv.FormatInt(gEnd-3600, 10)}, "until": {strconv.FormatInt(gEnd, 10)},
			"format": {"json"}, "maxDataPoints": {"200"},
		}
		hdr := http.Header{"Storage-Step": {"10s"}}
		same(t, "/render", v, http.MethodPost, hdr, "ObjectProxyCache")
		requireCacheHit(t, func() map[string]string {
			return same(t, "/render", v, http.MethodPost, hdr, "ObjectProxyCache").result
		})
		same(t, "/metrics/find", url.Values{"query": {"vmgraphite.*"}}, http.MethodGet, nil, "ObjectProxyCache")
	})
}
