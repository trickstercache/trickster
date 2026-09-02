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

package engines

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// benchLanes compares the ReverseProxy-backed passthrough lane against the
// hand-rolled DoProxy path on identical work, which is the measurement the
// lane split was deferred on: whether stdlib's per-request overhead
// (req.Clone, ClientTrace, loss of the ResponseWriter ReadFrom fast path)
// registers against the origin round trip.
//
// Caveat on the allocation numbers: the client-facing writer here is an
// httptest.ResponseRecorder, which does not implement io.ReaderFrom. That
// makes DoProxy's io.Copy allocate a fresh 32KB buffer per request, where a
// real *http.response would take one from net/http's sync.Pool. Lane A uses
// its configured BufferPool either way, so its allocation advantage on small
// bodies is an artifact of the harness, not a production result. The
// wall-clock comparison is unaffected.
func benchLanes(b *testing.B, bodySize int) {
	body := strings.Repeat("x", bodySize)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameContentType, "application/json")
		w.Header().Set(headers.NameContentLength, fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body)
	}))
	defer origin.Close()

	u, err := url.Parse(origin.URL)
	if err != nil {
		b.Fatal(err)
	}
	o := bo.New()
	o.Name = "bench"
	o.Provider = "rp"
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.PathPrefix = ""
	client, err := NewTestClient("bench", o, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	o.HTTPClient = client.HTTPClient()
	pc := po.New()

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, origin.URL+"/bench", nil)
		r.RequestURI = ""
		rsc := request.NewResources(o, pc, nil, nil, client, nil)
		return request.SetResources(r, rsc)
	}

	passthrough := NewPassthroughHandler(client)

	b.Run("LaneA_ReverseProxy", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(bodySize))
		for b.Loop() {
			w := httptest.NewRecorder()
			passthrough.ServeHTTP(w, newReq())
			if w.Body.Len() != bodySize {
				b.Fatalf("short body: %d", w.Body.Len())
			}
		}
	})

	b.Run("LaneB_DoProxy", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(bodySize))
		for b.Loop() {
			w := httptest.NewRecorder()
			DoProxy(w, newReq(), true)
			if w.Body.Len() != bodySize {
				b.Fatalf("short body: %d", w.Body.Len())
			}
		}
	})
}

func BenchmarkLanesSmallBody(b *testing.B)  { benchLanes(b, 1024) }
func BenchmarkLanesMediumBody(b *testing.B) { benchLanes(b, 64*1024) }
func BenchmarkLanesLargeBody(b *testing.B)  { benchLanes(b, 1024*1024) }
