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

package nlm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// NLM fanout clones the parent request N ways. For POST/PUT/PATCH the body
// must be primed before fanout, else N goroutines race on r.Body and
// rsc.RequestBody inside GetBody. Run under -race to catch any regression.
func TestNLMPostBodyFanoutIsRaceFree(t *testing.T) {
	const body = `{"query":"sum(rate(metric[5m]))","start":"2024-01-01T00:00:00Z","end":"2024-01-01T01:00:00Z","step":"15s"}`
	lm := "Sun, 06 Nov 1994 08:49:37 GMT"

	mk := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) != body {
				t.Errorf("%s: truncated body, got %d bytes want %d", name, len(b), len(body))
			}
			w.Header().Set(headers.NameLastModified, lm)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(name))
		})
	}

	p, _, st := albpool.New(-1, []http.Handler{mk("a"), mk("b"), mk("c"), mk("d")})
	for _, s := range st {
		s.Set(healthcheck.StatusPassing)
	}
	time.Sleep(250 * time.Millisecond)

	h := &handler{}
	h.SetPool(p)

	const callers = 16
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			r, _ := http.NewRequest(http.MethodPost, "http://trickstercache.org/api/v1/query_range", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("status %d", w.Code)
			}
		})
	}
	wg.Wait()
}
