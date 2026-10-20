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

package engines

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/promsim"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/rangesim"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	chunkConfigTSFactor    = 10
	chunkConfigBRChunkSize = 16
	chunkConfigSettle      = 2 * time.Second
	chunkConfigPoll        = 25 * time.Millisecond
)

var chunkConfigProviders = []string{providers.Memory, providers.Filesystem}

// writeChunkConfig writes a config for the default cache; a filesystem cache's objects can be counted on disk
func writeChunkConfig(t *testing.T, provider string, chunked bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	body := fmt.Sprintf(`
caches:
  default:
    provider: %s
    use_cache_chunking: %t
    timeseries_chunk_factor: %d
    byterange_chunk_size: %d
    filesystem:
      cache_path: %s
`, provider, chunked, chunkConfigTSFactor, chunkConfigBRChunkSize, cacheDir)
	path := filepath.Join(dir, "trickster.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, cacheDir
}

// settledFileCount waits for asynchronous cache writes to stop changing the cache directory
func settledFileCount(t *testing.T, provider, dir string) int {
	t.Helper()
	if provider != providers.Filesystem {
		time.Sleep(chunkConfigPoll)
		return 0
	}
	count := func() int {
		var n int
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		return n
	}
	last := -1
	for deadline := time.Now().Add(chunkConfigSettle); time.Now().Before(deadline); {
		time.Sleep(chunkConfigPoll * 4)
		if n := count(); n == last && n > 0 {
			return n
		} else {
			last = n
		}
	}
	return last
}

func TestDeltaProxyCacheChunkingFromConfig(t *testing.T) {
	for _, provider := range chunkConfigProviders {
		counts := make(map[bool]int, 2)
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%t", provider, chunked), func(t *testing.T) {
				path, cacheDir := writeChunkConfig(t, provider, chunked)
				testConfigFile = path
				ts, w, r, rsc, err := setupTestHarnessDPC()
				testConfigFile = ""
				if err != nil {
					t.Fatal(err)
				}
				defer closeTestHarness(ts, r)
				if got := rsc.CacheConfig.UseCacheChunking; got != chunked {
					t.Fatalf("use_cache_chunking from config = %t, want %t", got, chunked)
				}

				client := rsc.BackendClient.(*TestClient)
				rsc.BackendOptions.FastForwardDisable = true
				const step = 300 * time.Second
				end := time.Now().Add(-12 * time.Hour)
				extr := timeseries.Extent{Start: end.Add(-18 * time.Hour), End: end}

				fetch := func(wantStatus string) {
					t.Helper()
					extn := timeseries.Extent{Start: alignTime(extr.Start, step), End: alignTime(extr.End, step)}
					expected, _ := promsim.GetTimeSeriesData(queryReturnsOKNoLatency, extn.Start, extn.End, step)
					r.URL.Path = "/prometheus/api/v1/query_range"
					r.URL.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s", int(step.Seconds()),
						extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)
					w = httptest.NewRecorder()
					client.QueryRangeHandler(w, r)
					resp := w.Result()
					b, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Fatal(err)
					}
					if err := testStatusCodeMatch(resp.StatusCode, http.StatusOK); err != nil {
						t.Error(err)
					}
					if err := testStringMatch(string(b), expected); err != nil {
						t.Error(err)
					}
					if err := testResultHeaderPartMatch(resp.Header, map[string]string{keys.Status: wantStatus}); err != nil {
						t.Error(err)
					}
				}

				fetch(status.StatusKeyMiss)
				counts[chunked] = settledFileCount(t, provider, cacheDir)
				fetch(status.StatusHit)
				extr.End = extr.End.Add(time.Hour)
				fetch(status.StatusPartialHit)
				settledFileCount(t, provider, cacheDir)
				extr.Start = extr.Start.Add(-time.Hour)
				fetch(status.StatusPartialHit)
			})
		}
		// 18h at a 5m step and a chunk factor of 10 spans about 22 chunks, each its own cache object
		if provider == providers.Filesystem && counts[true] <= counts[false]+chunkConfigTSFactor {
			t.Errorf("chunked write stored %d files, unchunked %d: chunking did not take effect",
				counts[true], counts[false])
		}
	}
}

func TestObjectProxyCacheChunkingFromConfig(t *testing.T) {
	for _, provider := range chunkConfigProviders {
		counts := make(map[bool]int, 2)
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%t", provider, chunked), func(t *testing.T) {
				path, cacheDir := writeChunkConfig(t, provider, chunked)
				ts, _, r, rsc, err := setupTestHarnessOPCByType(path, "rangesim", "/byterange/opc", "", 0, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer closeTestHarness(ts, r)
				if got := rsc.CacheConfig.UseCacheChunking; got != chunked {
					t.Fatalf("use_cache_chunking from config = %t, want %t", got, chunked)
				}

				fetch := func(rng string, code int, wantStatus string) {
					t.Helper()
					want := rangesim.Body
					if rng != "" {
						r.Header.Set(headers.NameRange, rng)
						var err error
						if want, err = getExpectedRangeBody(r, ""); err != nil {
							t.Fatal(err)
						}
					} else {
						r.Header.Del(headers.NameRange)
					}
					_, errs := testFetchOPC(r, code, want, map[string]string{keys.Status: wantStatus})
					for _, err := range errs {
						t.Errorf("range %q: %v", rng, err)
					}
				}

				// each range crosses a byterange_chunk_size boundary
				fetch("bytes=0-40", http.StatusPartialContent, status.StatusKeyMiss)
				counts[chunked] = settledFileCount(t, provider, cacheDir)
				fetch("bytes=5-20", http.StatusPartialContent, status.StatusHit)
				fetch("bytes=30-60", http.StatusPartialContent, status.StatusPartialHit)
				// the partial hit read only chunks 16-63, so chunk 0-15 must survive its write-back intact
				fetch("bytes=0-10", http.StatusPartialContent, status.StatusHit)
				fetch("bytes=100-120", http.StatusPartialContent, status.StatusRangeMiss)
				settledFileCount(t, provider, cacheDir)
				fetch("", http.StatusOK, status.StatusPartialHit)
			})
		}
		// 41 bytes in 16-byte chunks is 3 chunk objects beside the meta document
		if provider == providers.Filesystem && counts[true] <= counts[false] {
			t.Errorf("chunked write stored %d files, unchunked %d: chunking did not take effect",
				counts[true], counts[false])
		}
	}
}
