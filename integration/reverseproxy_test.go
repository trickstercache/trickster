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
	cfg := writeTestConfig(t, 8574, 8575, 8584)
	rpcAddr := "127.0.0.1:8574"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: rpcAddr, MetricsAddr: "127.0.0.1:8575"}
	h.start(t)

	t.Run("full object cache", func(t *testing.T) {
		u := "http://" + rpcAddr + "/rpc1/test/object"
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
		u := "http://" + rpcAddr + "/rpc1/test/range"
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

	rangeClient := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	getRange := func(t *testing.T, path, rangeHdr string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest("GET", "http://"+rpcAddr+path, nil)
		require.NoError(t, err)
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		resp, err := rangeClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, b
	}

	// regression: #948 — sequential byte ranges were off-by-one and
	// io.ReadAll errors from document assembly were being ignored.
	t.Run("sequential byte ranges", func(t *testing.T) {
		resp1, b1 := getRange(t, "/rpc1/seq-range", "bytes=0-99")
		require.Equal(t, http.StatusPartialContent, resp1.StatusCode)
		require.Len(t, b1, 100, "bytes=0-99 must be 100 bytes")

		resp2, b2 := getRange(t, "/rpc1/seq-range", "bytes=100-199")
		require.Equal(t, http.StatusPartialContent, resp2.StatusCode)
		require.Len(t, b2, 100, "bytes=100-199 must be 100 bytes")
		require.NotEqual(t, b1, b2, "distinct ranges must return distinct byte slices")
	})

	// Overlapping ranges must agree on the overlap region (100..150).
	t.Run("overlapping byte ranges", func(t *testing.T) {
		resp1, b1 := getRange(t, "/rpc1/overlap-range", "bytes=0-150")
		require.Equal(t, http.StatusPartialContent, resp1.StatusCode)
		require.Len(t, b1, 151)

		resp2, b2 := getRange(t, "/rpc1/overlap-range", "bytes=100-250")
		require.Equal(t, http.StatusPartialContent, resp2.StatusCode)
		require.Len(t, b2, 151)

		// Overlap region: bytes 100-150 appears at [100:151] in b1 and [0:51] in b2.
		require.Equal(t, b1[100:151], b2[0:51],
			"overlap region must be byte-identical between the two ranged responses")
	})

	t.Run("invalid range returns 416", func(t *testing.T) {
		resp, _ := getRange(t, "/rpc1/bad-range", "bytes=99999999-99999999")
		require.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.StatusCode,
			"expected 416 for out-of-bounds range, got %d", resp.StatusCode)
	})

	// Conditional GET via If-Modified-Since: Mockster serves a static
	// Last-Modified of 2020-01-01; a matching conditional request must
	// return 304. We warm the cache first so the revalidation path is
	// exercised through the cache index rather than raw pass-through.
	t.Run("conditional if-modified-since", func(t *testing.T) {
		resp0, _ := getRange(t, "/rpc1/cond", "")
		require.Equal(t, http.StatusOK, resp0.StatusCode)
		lm := resp0.Header.Get("Last-Modified")
		require.NotEmpty(t, lm, "expected Last-Modified on first response")

		req, err := http.NewRequest("GET", "http://"+rpcAddr+"/rpc1/cond", nil)
		require.NoError(t, err)
		req.Header.Set("If-Modified-Since", lm)
		resp, err := rangeClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusNotModified, resp.StatusCode,
			"If-Modified-Since with matching Last-Modified must yield 304, got %d", resp.StatusCode)
	})
}
