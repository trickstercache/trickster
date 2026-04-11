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

// ruleHarness returns the harness for integration/testdata/configs/rule.yaml.
// Ports are in the 8550 band (see PORT RANGE for the rule-engine slice).
func ruleHarness() tricksterHarness {
	return tricksterHarness{
		ConfigPath:  "testdata/configs/rule.yaml",
		BaseAddr:    "127.0.0.1:8550",
		MetricsAddr: "127.0.0.1:8551",
	}
}

// instantQuery returns a stable "up" instant-query parameter set. Using a
// fixed query keeps all rule subtests apples-to-apples and avoids time
// skew between parallel runs.
func instantQuery() url.Values {
	return url.Values{"query": {"up"}}
}

// requireVectorResult asserts that the given promResponse is a successful
// instant-query result of the expected vector type. The actual vector
// contents are not asserted on — the point of the rule tests is that
// routing happened, not that Prometheus returned particular samples.
func requireVectorResult(t *testing.T, pr promResponse) {
	t.Helper()
	require.Equal(t, "success", pr.Status)
	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	require.Equal(t, "vector", qd.ResultType)
}

// TestRule_HeaderRoute exercises header-based rule routing:
// X-Route: a should land on prom-a, X-Route: b on prom-b. Both backends
// are real Prometheus pointing at the same origin, so we rely on a
// per-backend response_headers tag (X-Rule-Target) to prove which
// member was hit.
func TestRule_HeaderRoute(t *testing.T) {
	ruleHarness().start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	h := ruleHarness()

	t.Run("X-Route a routes to prom-a", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule1", "/api/v1/query",
			withParams(instantQuery()),
			withHeader("X-Route", "a"),
		)
		requireVectorResult(t, pr)
		require.Equal(t, "prom-a", hdr.Get("X-Rule-Target"),
			"expected X-Route=a to land on prom-a")
	})

	t.Run("X-Route b routes to prom-b", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule1", "/api/v1/query",
			withParams(instantQuery()),
			withHeader("X-Route", "b"),
		)
		requireVectorResult(t, pr)
		require.Equal(t, "prom-b", hdr.Get("X-Rule-Target"),
			"expected X-Route=b to land on prom-b")
	})
}

// TestRule_RegexExtract exercises regex-based (string-rmatch) rule
// routing on the User-Agent header. A UA containing "Grafana" should
// route to prom-b; all other UAs (including the default Go HTTP client)
// should fall through to prom-a via next_route.
func TestRule_RegexExtract(t *testing.T) {
	ruleHarness().start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	h := ruleHarness()

	t.Run("Grafana UA routes to prom-b", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule-ua", "/api/v1/query",
			withParams(instantQuery()),
			withHeader("User-Agent", "Grafana/10.4.0"),
		)
		requireVectorResult(t, pr)
		require.Equal(t, "prom-b", hdr.Get("X-Rule-Target"),
			"expected Grafana UA to land on prom-b via rmatch")
	})

	t.Run("non-Grafana UA falls through to prom-a", func(t *testing.T) {
		pr, hdr := h.queryProm(t, "rule-ua", "/api/v1/query",
			withParams(instantQuery()),
			withHeader("User-Agent", "curl/8.4.0"),
		)
		requireVectorResult(t, pr)
		require.Equal(t, "prom-a", hdr.Get("X-Rule-Target"),
			"expected non-Grafana UA to land on prom-a (default next_route)")
	})
}

// TestRule_DefaultCase verifies that when no case matches, the rule
// falls through to its configured next_route. by-default inspects a
// header the test never sends, so the default (prom-b) must fire.
func TestRule_DefaultCase(t *testing.T) {
	ruleHarness().start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	h := ruleHarness()

	pr, hdr := h.queryProm(t, "rule-default", "/api/v1/query",
		withParams(instantQuery()),
	)
	requireVectorResult(t, pr)
	require.Equal(t, "prom-b", hdr.Get("X-Rule-Target"),
		"expected unmatched input to fall through to default next_route (prom-b)")
}
