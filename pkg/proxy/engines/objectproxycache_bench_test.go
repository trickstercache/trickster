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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func BenchmarkObjectProxyCache(b *testing.B) {
	license, err := os.Open("../../../LICENSE")
	if err != nil {
		b.Fatal(err)
	}
	body, err := io.ReadAll(license)
	if err != nil {
		b.Fatal(err)
	}
	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", string(body), http.StatusPartialContent, hdrs)
	if err != nil {
		b.Error(err)
	}
	defer ts.Close()

	r.Header.Add(headers.NameRange, "bytes=0-10000")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		ObjectProxyCacheRequest(w, r)
	}
}

func BenchmarkObjectProxyCacheChunks(b *testing.B) {
	license, err := os.Open("../../../LICENSE")
	if err != nil {
		b.Fatal(err)
	}
	body, err := io.ReadAll(license)
	if err != nil {
		b.Fatal(err)
	}
	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", string(body), http.StatusPartialContent, hdrs)
	if err != nil {
		b.Error(err)
	}
	rsc.CacheConfig.UseCacheChunking = true
	defer ts.Close()

	r.Header.Add(headers.NameRange, "bytes=0-10000")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		ObjectProxyCacheRequest(w, r)
	}
}
