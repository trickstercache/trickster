/*
 * Copyright 2026 The Trickster Authors
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

package http3

import (
	"io"
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const (
	benchBodySize    = 64 * 1024
	benchContentType = "application/octet-stream"
)

func BenchmarkHTTP3RoundTrip(b *testing.B) {
	body := make([]byte, benchBodySize)
	client, addr, stop := testEndpoint(b, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameContentType, benchContentType)
		_, _ = w.Write(body)
	}))
	defer stop()

	// one warm-up request establishes the QUIC connection outside the timed loop
	resp, err := client.Get(addr)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	b.SetBytes(benchBodySize)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		resp, err := client.Get(addr)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		_ = resp.Body.Close()
	}
}
