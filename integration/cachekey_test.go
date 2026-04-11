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

// TestCacheKey_POST_RawQuery validates that a POST range query with its
// parameters carried in a form body survives Trickster's SetRequestValues
// rewrite: the upstream request must carry the normalized params in its URL
// RawQuery so the range_query path matcher and DeltaProxyCache engine both
// pick up the request. Before the #969 fix, SetRequestValues skipped updating
// r.URL.RawQuery for methods with bodies, so any downstream code that read
// the upstream request URL (including path matching and DPC routing) saw a
// stale query string.
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
	// The form body carries every range query param. A unique query suffix
	// forces a kmiss on the first call so we see the DPC engine rather than
	// an unrelated cached entry from an earlier sub-test.
	form := url.Values{
		"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
		"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
		"end":   {fmt.Sprintf("%d", now.Unix())},
		"step":  {"15"},
	}
	u := "http://" + tricksterAddr + "/prom1/api/v1/query_range"
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Post(u, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"unexpected status %d: %s", resp.StatusCode, string(body))

	var pr promResponse
	require.NoError(t, json.Unmarshal(body, &pr))
	require.Equal(t, "success", pr.Status)
	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	require.Equal(t, "matrix", qd.ResultType)

	result := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
	t.Logf("POST form body range query: %v", result)
	require.Equal(t, "DeltaProxyCache", result["engine"],
		"POST range query with a form body must route through DeltaProxyCache (issue #969)")
}
