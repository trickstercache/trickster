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
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReverseProxyCache tests reverse proxy cache with byte-range support.
// Requires: make developer-start (for Mockster on :8482).
func TestReverseProxyCache(t *testing.T) {
	developerHarness().start(t)

	t.Run("full object cache", func(t *testing.T) {
		u := "http://" + tricksterAddr + "/rpc1/test/object"
		// First request: cache miss
		resp, err := http.Get(u)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.NotEmpty(t, body)
		result := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		t.Logf("rpc first: %s", resp.Header.Get("X-Trickster-Result"))

		// Second request: should be a cache hit
		resp2, err := http.Get(u)
		require.NoError(t, err)
		body2, err := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp2.StatusCode)
		require.Equal(t, body, body2, "cached response should match original")
		result2 := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
		t.Logf("rpc second: %s", resp2.Header.Get("X-Trickster-Result"))
		_ = result
		_ = result2
	})

	t.Run("byte range request", func(t *testing.T) {
		u := "http://" + tricksterAddr + "/rpc1/test/range"
		req, err := http.NewRequest("GET", u, nil)
		require.NoError(t, err)
		req.Header.Set("Range", "bytes=0-99")

		// Use a transport that doesn't auto-decompress
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		t.Logf("byte range: status=%d, Content-Range=%s, X-Trickster-Result=%s",
			resp.StatusCode, resp.Header.Get("Content-Range"), resp.Header.Get("X-Trickster-Result"))

		// Mockster's /byterange endpoint should return 206 for range requests
		require.Equal(t, http.StatusPartialContent, resp.StatusCode,
			"expected 206 Partial Content for Range request, body: %s", string(body))
		require.NotEmpty(t, resp.Header.Get("Content-Range"),
			"expected Content-Range header in 206 response")
		require.Len(t, body, 100, "expected 100 bytes for Range: bytes=0-99")
	})
}
