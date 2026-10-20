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

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUseCacheChunking(t *testing.T) {
	const body = `
backends:
  chunked:
    provider: prometheus
    origin_url: http://prom:9090
    cache_name: chunked
  unchunked:
    provider: prometheus
    origin_url: http://prom:9091
    cache_name: unchunked
caches:
  chunked:
    provider: memory
    use_cache_chunking: true
    timeseries_chunk_factor: 7
    byterange_chunk_size: 2048
  unchunked:
    provider: memory
`
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	ch, un := c.Caches["chunked"], c.Caches["unchunked"]
	if ch == nil || un == nil {
		t.Fatalf("caches = %v", c.Caches)
	}
	if !ch.UseCacheChunking || ch.TimeseriesChunkFactor != 7 || ch.ByterangeChunkSize != 2048 {
		t.Errorf("chunked cache lost its settings: %+v", ch)
	}
	if un.UseCacheChunking {
		t.Error("an unset use_cache_chunking must load as false")
	}
}
