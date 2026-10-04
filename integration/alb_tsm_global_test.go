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
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

const (
	tsmGlobalRequireEnv = "TRICKSTER_TSM_GLOBAL_TEST"
	thanosQueryURL      = "http://127.0.0.1:11903"
	mimirPodCountQuery  = "count(count by (pod) (tsm_pod_cpu_seconds_total))"
	mimirPodRateQuery   = "sum(rate(tsm_pod_cpu_seconds_total[5m]))"
	// mimirPods is the distinct pods the two seeded regions hold between them
	mimirPods = 5
	// mimirMergedRates is what summing regional rates gives: the shared pod and both backups count twice
	mimirMergedRates = 8
)

// requireTSMGlobalStack skips unless the mimir and thanos compose profiles are running, or fails when
// TRICKSTER_TSM_GLOBAL_TEST=1 requires them
func requireTSMGlobalStack(t *testing.T) {
	t.Helper()
	for _, u := range []string{
		"http://127.0.0.1:11080/ready", "http://127.0.0.1:11081/ready",
		thanosQueryURL + "/-/ready",
	} {
		resp, err := http.Get(u) //nolint:noctx // a readiness probe
		if err == nil {
			resp.Body.Close()
		}
		if err == nil && resp.StatusCode == http.StatusOK {
			continue
		}
		if os.Getenv(tsmGlobalRequireEnv) == "1" {
			t.Fatalf("%s is not ready", u)
		}
		t.Skipf("the mimir and thanos compose profiles are not running (%s); "+
			"run make developer-start-mimir developer-start-thanos", u)
	}
}

// promSamples maps each result series' labels to its values in time order
type promSamples map[string][]float64

func getPromSamples(t *testing.T, base string, apiPath string, params url.Values) (promSamples, http.Header) {
	t.Helper()
	resp, err := http.Get(base + apiPath + "?" + params.Encode()) //nolint:noctx // a test query
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", params.Get("query"), body)
	var pr promResponse
	require.NoError(t, json.Unmarshal(body, &pr))
	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	var series []struct {
		Metric map[string]string `json:"metric"`
		Value  []any             `json:"value"`
		Values [][]any           `json:"values"`
	}
	require.NoError(t, json.Unmarshal(qd.Result, &series))
	out := make(promSamples, len(series))
	for _, s := range series {
		keys := make([]string, 0, len(s.Metric))
		for k, v := range s.Metric {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		points := s.Values
		if s.Value != nil {
			points = [][]any{s.Value}
		}
		values := make([]float64, len(points))
		for i, p := range points {
			v, err := strconv.ParseFloat(p[1].(string), 64)
			require.NoError(t, err)
			values[i] = v
		}
		out[fmt.Sprint(keys)] = values
	}
	return out, resp.Header
}

func requireCloseSamples(t *testing.T, want, got promSamples, msg string) {
	t.Helper()
	require.Len(t, got, len(want), "%s: series", msg)
	for k, wv := range want {
		gv, ok := got[k]
		require.True(t, ok, "%s: missing series %s", msg, k)
		require.Len(t, gv, len(wv), "%s: %s points", msg, k)
		for i := range wv {
			require.InEpsilon(t, wv[i]+1, gv[i]+1, 1e-9, "%s: %s point %d", msg, k, i)
		}
	}
}

func TestALBTSMGlobalEvaluation(t *testing.T) {
	requireTSMGlobalStack(t)
	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(fmt.Sprintf(albTestdata(t, "alb_tsm_global/config.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort)), 0o644))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	runTrickster(t, ctx, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))
	front := fmt.Sprintf("http://127.0.0.1:%d", frontPort)
	// a minute ago on a 15s boundary, so every scrape and sample it reads has landed
	at := time.Now().Add(-time.Minute).Truncate(15 * time.Second)
	instant := func(query string) url.Values {
		return url.Values{"query": {query}, "time": {strconv.FormatInt(at.Unix(), 10)}}
	}

	t.Run("mimir union and deduplication", func(t *testing.T) {
		got, _ := getPromSamples(t, front+"/mimir-global", "/api/v1/query", instant(mimirPodCountQuery))
		require.Equal(t, promSamples{"[]": {mimirPods}}, got)
		got, _ = getPromSamples(t, front+"/mimir-merge", "/api/v1/query", instant(mimirPodCountQuery))
		require.Equal(t, promSamples{"[]": {mimirPods}}, got, "merge mode unions the inner pod label sets")

		got, _ = getPromSamples(t, front+"/mimir-global", "/api/v1/query", instant(mimirPodRateQuery))
		require.InDelta(t, mimirPods, got["[]"][0], 0.01, "global evaluation deduplicates the backups")
		got, _ = getPromSamples(t, front+"/mimir-merge", "/api/v1/query", instant(mimirPodRateQuery))
		require.InDelta(t, mimirMergedRates, got["[]"][0], 0.01, "merging regional sums counts copies twice")
	})

	t.Run("cached global ranges", func(t *testing.T) {
		params := url.Values{
			"query": {mimirPodRateQuery}, "start": {strconv.FormatInt(at.Add(-30*time.Minute).Unix(), 10)},
			"end": {strconv.FormatInt(at.Add(-10*time.Minute).Unix(), 10)}, "step": {"60"},
		}
		first, h := getPromSamples(t, front+"/mimir-global", "/api/v1/query_range", params)
		require.Contains(t, h.Get(headers.NameTricksterResult), "status=kmiss")
		again, h := getPromSamples(t, front+"/mimir-global", "/api/v1/query_range", params)
		require.Contains(t, h.Get(headers.NameTricksterResult), "status=hit")
		requireCloseSamples(t, first, again, "cached range")
	})

	t.Run("thanos parity", func(t *testing.T) {
		for _, query := range []string{
			"count(up)",
			"sum by (job) (rate(prometheus_http_requests_total[5m]))",
			"rate(prometheus_tsdb_head_samples_appended_total[5m])",
			"max_over_time(prometheus_tsdb_head_series[5m])",
			"histogram_quantile(0.9, sum by (le) (rate(prometheus_http_request_duration_seconds_bucket[5m])))",
		} {
			want, _ := getPromSamples(t, thanosQueryURL, "/api/v1/query", instant(query))
			got, _ := getPromSamples(t, front+"/thanos-global", "/api/v1/query", instant(query))
			requireCloseSamples(t, want, got, query)
			// a range reads each replica's whole history, through any replica switches; its ends are on
			// the step, where Trickster aligns them
			end := at.Truncate(30 * time.Second)
			params := url.Values{
				"query": {query}, "start": {strconv.FormatInt(end.Add(-20*time.Minute).Unix(), 10)},
				"end": {strconv.FormatInt(end.Unix(), 10)}, "step": {"30"},
			}
			want, _ = getPromSamples(t, thanosQueryURL, "/api/v1/query_range", params)
			got, _ = getPromSamples(t, front+"/thanos-global", "/api/v1/query_range", params)
			requireCloseSamples(t, want, got, query+" range")
		}
	})
}
