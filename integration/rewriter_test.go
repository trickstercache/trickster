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
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRequestRewriter tests the request rewriting feature.
// Uses a config that applies the range-to-instant rewriter to /api/v1/query_range,
// converting it into an instant query (/api/v1/query).
// Requires: make developer-start (for Prometheus on :9090).
func TestRequestRewriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/rewriter.yaml")
	waitForTrickster(t, "127.0.0.1:8494")
	waitForPrometheusData(t, "127.0.0.1:9090")

	const rewriterAddr = "127.0.0.1:8493"

	t.Run("range to instant rewrite", func(t *testing.T) {
		now := time.Now()
		// Send a range query — the rewriter should convert it to an instant query.
		params := url.Values{
			"query": {"up"},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		pr, hdr := queryTricksterProm(t, rewriterAddr, "prom1", "/api/v1/query_range", params)
		require.Equal(t, "success", pr.Status)

		// The rewriter converts query_range → query, so the result should be
		// a vector (instant query) instead of a matrix (range query).
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType,
			"rewriter should have converted range query to instant query (vector result)")
		t.Logf("rewriter result: %s", hdr.Get("X-Trickster-Result"))
	})
}
