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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCacheKey_LabelValues_MatchParam validates that two /label/<name>/values
// requests with different match[] parameters produce different cache keys.
// Before the fix, both requests would hit the same cache entry.
//
// regression: #965
func TestCacheKey_LabelValues_MatchParam(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{},
		"-config", "../docs/developer/environment/trickster-config/trickster.yaml")
	waitForTrickster(t, "127.0.0.1:8481")
	waitForPrometheusData(t, "127.0.0.1:9090")

	fetch := func(t *testing.T, match string) map[string]string {
		t.Helper()
		u := "http://" + tricksterAddr + "/prom1/api/v1/label/job/values?" +
			url.Values{"match[]": {match}}.Encode()
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"unexpected status %d for match[]=%s: %s", resp.StatusCode, match, string(body))
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		return parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
	}

	// First request with match[]=up -- must be a miss (kmiss / miss).
	r1 := fetch(t, "up")
	t.Logf("match[]=up: %v", r1)
	require.NotEqual(t, "hit", r1["status"],
		"first request must not be a hit, got %v", r1)

	// Second request with a different match[] value -- before the #965 fix
	// this would collide with the first and return hit. After the fix, the
	// match[] param participates in the cache key so this is a fresh miss.
	r2 := fetch(t, "process_cpu_seconds_total")
	t.Logf("match[]=process_cpu_seconds_total: %v", r2)
	require.NotEqual(t, "hit", r2["status"],
		"second request with a different match[] must not collide with the first (issue #965), got %v", r2)
}

// TestCacheKey_POST_RawQuery validates the full split-params POST path:
//   - #969: SetRequestValues must sync the form body back into r.URL.RawQuery
//     so path matching and DPC routing see the normalized query string
//   - #969 read-side follow-up: GetRequestValues must MERGE r.URL.Query()
//     with r.PostForm so a client that carries `step` in the URL and the
//     rest in the body doesn't lose `step` on its way into cache key
//     derivation and time-range extraction
//
// The test POSTs with `step` in the URL and query/start/end in the form body,
// then re-POSTs with a different `step` to prove that `step` actually
// participates in the cache key (if it were dropped, the second call would
// be a hit on the first's entry).
//
// regression: #969
func TestCacheKey_POST_RawQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{},
		"-config", "../docs/developer/environment/trickster-config/trickster.yaml")
	waitForTrickster(t, "127.0.0.1:8481")
	waitForPrometheusData(t, "127.0.0.1:9090")

	now := time.Now()
	// Unique query suffix so we get a kmiss on the first call regardless
	// of anything cached by earlier tests.
	queryExpr := fmt.Sprintf("up + 0*%d", now.UnixNano())
	body := url.Values{
		"query": {queryExpr},
		"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
		"end":   {fmt.Sprintf("%d", now.Unix())},
	}.Encode()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

	post := func(t *testing.T, step string) map[string]string {
		t.Helper()
		u := "http://" + tricksterAddr + "/prom1/api/v1/query_range?step=" + step
		resp, err := client.Post(u, "application/x-www-form-urlencoded",
			strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		rb, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"unexpected status %d: %s", resp.StatusCode, string(rb))
		var pr promResponse
		require.NoError(t, json.Unmarshal(rb, &pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType,
			"split-params POST must reach the range handler (step in URL must survive)")
		return parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
	}

	// First: step=15 in URL, everything else in form body. Must route through
	// DPC (proves step survived the URL→form merge) and must be a cold miss.
	r1 := post(t, "15")
	t.Logf("step=15 split POST: %v", r1)
	require.Equal(t, "DeltaProxyCache", r1["engine"],
		"split-params POST must route through DeltaProxyCache (#969 read side)")

	// Second: same request, should cache-hit.
	r2 := post(t, "15")
	t.Logf("step=15 repeat: %v", r2)
	require.Equal(t, "hit", r2["status"],
		"repeat of identical split-params POST must hit the cache")

	// Third: same body, DIFFERENT step in URL. If step were dropped on read,
	// the cache key would collide with r1 and this would be a hit. The fix
	// preserves step in the merged param set, so the cache key differs and
	// this is a fresh miss.
	r3 := post(t, "30")
	t.Logf("step=30 split POST: %v", r3)
	require.NotEqual(t, "hit", r3["status"],
		"changing step in URL must produce a distinct cache key (proves URL params are preserved in GetRequestValues)")
}
