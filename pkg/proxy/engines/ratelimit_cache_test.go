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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	rlhandler "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/handler"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"
)

func TestRateLimitChargesCacheHit(t *testing.T) {
	hdrs := map[string]string{headers.NameCacheControl: "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusOK, hdrs)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestHarness(ts, r)

	o := &options.Options{Name: "cache-hit-limit", Limit: 2, Window: timeconv.Duration(time.Minute)}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	// Resources are attached outside the limiter, and the cache handler is inside it.
	h := middleware.WithResourcesContext(rsc.BackendClient, rsc.BackendOptions, rsc.CacheClient,
		rsc.PathConfig, rsc.Tracer, rlhandler.HTTP(o, nil, http.HandlerFunc(ObjectProxyCacheRequest)))
	base := request.ClearResources(r)
	for i, want := range []status.Label{status.StatusKeyMiss, status.StatusHit} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, base.Clone(base.Context()))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status %d", i+1, rec.Code)
		}
		if err := testResultHeaderPartMatch(rec.Header(), map[string]string{keys.Status: string(want)}); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, base.Clone(base.Context()))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("hit past the limit = %d", rec.Code)
	}
}
