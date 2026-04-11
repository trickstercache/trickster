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
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPurge_ByKey verifies the mgmt purge-by-path API invalidates cached
// objects stored under the ObjectProxyCache engine. The prom /api/v1/rules
// endpoint is used because it is registered with an empty CacheKeyParams
// slice, so its derived cache key only hashes "method.GET." — which is
// exactly what the purge-by-path handler recomputes before calling
// cache.Remove. Endpoints like /api/v1/query fold query params into the
// key hash and therefore can't be reached by purge-by-path.
func TestPurge_ByKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/configs/purge.yaml")

	const (
		frontAddr   = "127.0.0.1:8539"
		metricsAddr = "127.0.0.1:8540"
		mgmtAddr    = "127.0.0.1:8541"
	)
	waitForTrickster(t, metricsAddr)
	waitForPrometheusData(t, "127.0.0.1:9090")

	// /api/v1/rules is registered with empty CacheKeyParams, so its
	// cache key is deterministic from path+method alone.
	const apiPath = "/api/v1/rules"

	// 1) Warm the cache — expect kmiss.
	_, hdr1 := queryTricksterProm(t, frontAddr, "prom1", apiPath, nil)
	res1 := parseTricksterResult(hdr1.Get("X-Trickster-Result"))
	t.Logf("first request: %s", hdr1.Get("X-Trickster-Result"))
	require.Equal(t, "ObjectProxyCache", res1["engine"])
	require.Equal(t, "kmiss", res1["status"])

	// 2) Re-issue — expect hit.
	_, hdr2 := queryTricksterProm(t, frontAddr, "prom1", apiPath, nil)
	res2 := parseTricksterResult(hdr2.Get("X-Trickster-Result"))
	t.Logf("second request: %s", hdr2.Get("X-Trickster-Result"))
	require.Equal(t, "hit", res2["status"])

	// 3) Issue a purge-by-path against the mgmt listener. The handler
	// path prefix is /trickster/purge/path/ followed by {backend}/{path};
	// it recomputes cache keys for all OPC/DPC engine + HTTP method
	// combinations and calls cache.Remove on each.
	purgeURL := fmt.Sprintf("http://%s/trickster/purge/path/prom1%s", mgmtAddr, apiPath)
	req, err := http.NewRequest(http.MethodGet, purgeURL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"purge call failed: %s", strings.TrimSpace(string(body)))
	t.Logf("purge response: %s", strings.TrimSpace(string(body)))

	// 4) Re-issue the same request — expect kmiss again (cache entry
	// was evicted).
	_, hdr3 := queryTricksterProm(t, frontAddr, "prom1", apiPath, nil)
	res3 := parseTricksterResult(hdr3.Get("X-Trickster-Result"))
	t.Logf("after-purge request: %s", hdr3.Get("X-Trickster-Result"))
	require.NotEqual(t, "hit", res3["status"],
		"expected cache entry to be evicted after purge; got %s", res3["status"])
}
