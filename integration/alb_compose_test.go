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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestALBCompose(t *testing.T) {
	const promResp = `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up","job":"%s"},"value":[1700000000,"1"]}]}}`

	mkLeaf := func(name string, hits *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/status/buildinfo":
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"success","data":{"version":"2.0"}}`)
			default:
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, promResp, name)
			}
		}))
	}

	var leafAHits, leafBHits atomic.Int64
	leafA := mkLeaf("leafA", &leafAHits)
	leafB := mkLeaf("leafB", &leafBHits)
	t.Cleanup(leafA.Close)
	t.Cleanup(leafB.Close)

	frontPort := 18700
	metricsPort := 18701
	mgmtPort := 18702

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
authenticators:
  alb-ur-users:
    provider: basic
    proxy_preserve: true
    users:
      bob: bobpw
backends:
  prom-leaf:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
  prom-leaf-b:
    provider: prometheus
    origin_url: %s
    cache_name: mem1
  alb-inner:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - prom-leaf
  alb-outer:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - alb-inner
  rule-to-leafB:
    provider: rule
    rule_name: route-all-to-leafB
  alb-with-rule:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - rule-to-leafB
  rule-bob:
    provider: rule
    rule_name: route-all-to-leaf
  alb-ur:
    provider: alb
    authenticator_name: alb-ur-users
    alb:
      mechanism: ur
      user_router:
        default_backend: prom-leaf
        users:
          bob:
            to_backend: rule-bob
  alb-fr-leaf:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - prom-leaf
  rule-to-alb:
    provider: rule
    rule_name: route-all-to-alb
rules:
  route-all-to-leaf:
    input_source: header
    input_key: X-Never-Set
    input_type: string
    operation: eq
    next_route: prom-leaf
    cases:
      - matches:
          - 'never-matches'
        next_route: prom-leaf
  route-all-to-leafB:
    input_source: header
    input_key: X-Never-Set
    input_type: string
    operation: eq
    next_route: prom-leaf-b
    cases:
      - matches:
          - 'never-matches'
        next_route: prom-leaf-b
  route-all-to-alb:
    input_source: header
    input_key: X-Never-Set
    input_type: string
    operation: eq
    next_route: alb-fr-leaf
    cases:
      - matches:
          - 'never-matches'
        next_route: alb-fr-leaf
`, frontPort, metricsPort, mgmtPort, leafA.URL, leafB.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	frontAddr := fmt.Sprintf("127.0.0.1:%d", frontPort)

	// distinct query per subtest to bypass shared cache
	getProm := func(t *testing.T, backend, q string, hdrs map[string]string) (*http.Response, []byte) {
		t.Helper()
		u := fmt.Sprintf("http://%s/%s/api/v1/query?query=%s",
			frontAddr, backend, url.QueryEscape(q))
		req, err := http.NewRequest(http.MethodGet, u, nil)
		require.NoError(t, err)
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, body
	}

	requireValidPromVector := func(t *testing.T, body []byte) {
		t.Helper()
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr), "body: %s", string(body))
		require.Equal(t, "success", pr.Status, "body: %s", string(body))
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series, "expected non-empty vector")
	}

	t.Run("A1_alb_of_alb", func(t *testing.T) {
		baseline := leafAHits.Load()
		resp, body := getProm(t, "alb-outer", "up_a1", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		if resp.StatusCode == http.StatusOK {
			requireValidPromVector(t, body)
		}
		assert.Greater(t, leafAHits.Load(), baseline,
			"leafA should have received a request via alb-outer -> alb-inner -> prom-leaf")
	})

	t.Run("A2_alb_pool_with_rule", func(t *testing.T) {
		baseline := leafBHits.Load()
		resp, body := getProm(t, "alb-with-rule", "up_a2", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		if resp.StatusCode == http.StatusOK {
			requireValidPromVector(t, body)
		}
		assert.Greater(t, leafBHits.Load(), baseline,
			"leafB should have received a request via alb-with-rule -> rule-to-leafB -> prom-leaf-b")
	})

	t.Run("A3_ur_routes_to_rule", func(t *testing.T) {
		baseline := leafAHits.Load()
		authz := "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:bobpw"))
		resp, body := getProm(t, "alb-ur", "up_a3", map[string]string{"Authorization": authz})
		assert.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		if resp.StatusCode == http.StatusOK {
			requireValidPromVector(t, body)
		}
		assert.Greater(t, leafAHits.Load(), baseline,
			"leafA should have received a request via alb-ur -> rule-bob -> prom-leaf")
	})

	t.Run("V1_rule_routes_to_alb", func(t *testing.T) {
		baseline := leafAHits.Load()
		resp, body := getProm(t, "rule-to-alb", "up_v1", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		if resp.StatusCode == http.StatusOK {
			requireValidPromVector(t, body)
		}
		assert.Greater(t, leafAHits.Load(), baseline,
			"leafA should have received a request via rule-to-alb -> alb-fr-leaf -> prom-leaf")
	})
}
