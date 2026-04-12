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
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRule(t *testing.T) {
	h := tricksterHarness{
		ConfigPath:  "testdata/configs/rule.yaml",
		BaseAddr:    "127.0.0.1:8550",
		MetricsAddr: "127.0.0.1:8551",
	}
	h.start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	instantQuery := url.Values{"query": {"up"}}

	requireVectorResult := func(t *testing.T, pr promResponse) {
		t.Helper()
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
	}

	t.Run("header route a to prom-a", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule1", "/api/v1/query",
			withParams(instantQuery), withHeader("X-Route", "a"))
		requireVectorResult(t, pr)
		require.Equal(t, "prom-a", hdr.Get("X-Rule-Target"),
			"expected X-Route=a to land on prom-a")
	})

	t.Run("header route b to prom-b", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule1", "/api/v1/query",
			withParams(instantQuery), withHeader("X-Route", "b"))
		requireVectorResult(t, pr)
		require.Equal(t, "prom-b", hdr.Get("X-Rule-Target"),
			"expected X-Route=b to land on prom-b")
	})

	t.Run("regex Grafana UA routes to prom-b", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule-ua", "/api/v1/query",
			withParams(instantQuery), withHeader("User-Agent", "Grafana/10.4.0"))
		requireVectorResult(t, pr)
		require.Equal(t, "prom-b", hdr.Get("X-Rule-Target"),
			"expected Grafana UA to land on prom-b via rmatch")
	})

	t.Run("regex non-Grafana UA falls to prom-a", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule-ua", "/api/v1/query",
			withParams(instantQuery), withHeader("User-Agent", "curl/8.4.0"))
		requireVectorResult(t, pr)
		require.Equal(t, "prom-a", hdr.Get("X-Rule-Target"),
			"expected non-Grafana UA to land on prom-a (default next_route)")
	})

	t.Run("default case falls to prom-b", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule-default", "/api/v1/query",
			withParams(instantQuery))
		requireVectorResult(t, pr)
		require.Equal(t, "prom-b", hdr.Get("X-Rule-Target"),
			"expected unmatched input to fall through to default next_route (prom-b)")
	})
}
