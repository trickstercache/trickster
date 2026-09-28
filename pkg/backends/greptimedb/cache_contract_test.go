/*
 * Copyright 2026 The Trickster Authors
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package greptimedb

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestPrometheusCredentialIsolation(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, endpoint := range []string{"query", "series", "labels", "label/job/values"} {
			if method == http.MethodPost && endpoint == "label/job/values" {
				continue
			}
			for _, policy := range []string{"", "private, max-age=60", "public, max-age=60"} {
				t.Run(method+"/"+endpoint+"/"+policy, func(t *testing.T) {
					var calls atomic.Int32
					shared := strings.HasPrefix(policy, "public")
					h := newHTTPHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						user := r.Header.Get("Authorization")
						if shared {
							user = "public"
						}
						w.Header().Set("Content-Type", "application/json")
						if policy != "" {
							w.Header().Set("Cache-Control", policy)
						}
						fmt.Fprintf(w, `{"status":"success","data":[%q]}`, user)
					}))
					values := url.Values{"query": {"up"}, "match[]": {"up"}, "time": {"1704067200"}}
					for _, user := range []string{"Basic dXNlcjph", "Basic dXNlcjpi", "Basic dXNlcjph", "Basic dXNlcjpi", ""} {
						w := h.promQuery(t, method, endpoint, values, http.Header{"Authorization": {user}})
						want := user
						if shared {
							want = "public"
						}
						if body := fmt.Sprintf(`{"status":"success","data":[%q]}`, want); w.Code != 200 || w.Body.String() != body {
							t.Fatalf("credential-specific response changed: code=%d body=%s want=%s", w.Code, w.Body.String(), body)
						}
					}
					if shared && method == http.MethodGet && calls.Load() != 1 {
						t.Fatalf("explicit origin sharing was lost: %d origin requests", calls.Load())
					}
				})
			}
		}
	}
}

func TestHTTPSQLMarshalFailurePreservesFallback(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			origin := &httpOrigin{}
			h := newHTTPHarness(t, origin)
			marshal := h.client.sqlModeler.WireMarshalWriter
			h.client.sqlModeler.WireMarshalWriter = func(_ timeseries.Timeseries, _ *timeseries.RequestOptions, _ int, w io.Writer) error {
				_, _ = io.WriteString(w, "partial invalid result")
				return errors.New("injected marshal failure")
			}
			start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
			statement := liveRange(start, start.Add(time.Hour))
			w := h.query(t, method, statement, nil, nil)
			assertHTTPResult(t, w, "HTTPProxy", "proxy-only", 4)
			if strings.Contains(w.Body.String(), "partial invalid result") || w.Header().Get("X-Greptime-Execution-Time") != "37" {
				t.Fatal("failed pre-render leaked into the original response")
			}
			if got := origin.snapshot(); len(got) != 2 || got[1] != statement {
				t.Fatalf("fallback did not replay original SQL: %v", got)
			}
			h.client.sqlModeler.WireMarshalWriter = marshal
			assertHTTPResult(t, h.query(t, method, statement, nil, nil), "DeltaProxyCache", "kmiss", 4)
			assertHTTPResult(t, h.query(t, method, statement, nil, nil), "DeltaProxyCache", "hit", 4)
		})
	}
}

func TestHTTPSQLSerializesOnce(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			h := newHTTPHarness(t, &httpOrigin{})
			var calls atomic.Int32
			marshal := h.client.sqlModeler.WireMarshalWriter
			h.client.sqlModeler.WireMarshalWriter = func(ts timeseries.Timeseries, ro *timeseries.RequestOptions, status int, w io.Writer) error {
				calls.Add(1)
				return marshal(ts, ro, status, w)
			}
			start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
			for _, tc := range []struct {
				status string
				end    time.Time
				rows   int
				header http.Header
			}{
				{"kmiss", start.Add(time.Hour), 4, nil},
				{"hit", start.Add(time.Hour), 4, nil},
				{"phit", start.Add(75 * time.Minute), 5, nil},
				{"purge", start.Add(75 * time.Minute), 5, http.Header{"Cache-Control": {"no-cache"}}},
			} {
				before := calls.Load()
				w := h.query(t, method, liveRange(start, tc.end), nil, tc.header)
				assertHTTPResult(t, w, "DeltaProxyCache", tc.status, tc.rows)
				if got := calls.Load() - before; got != 1 {
					t.Errorf("%s serialized %d times, want 1", tc.status, got)
				}
			}
		})
	}
}
