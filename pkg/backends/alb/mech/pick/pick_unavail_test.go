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

package pick

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// rr must not route to a member whose status dropped below the pool's healthyFloor: the very
// next request after the transition already excludes it, with no wait for a refresh.
func TestNextTargetSkipsFailingTargetImmediately(t *testing.T) {
	var hits1, hits2 atomic.Int64
	h1 := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hits1.Add(1) })
	h2 := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { hits2.Add(1) })

	p, _, sts := albpool.New(1, []http.Handler{h1, h2})
	defer p.Stop()

	sts[0].Set(healthcheck.StatusPassing)
	sts[1].Set(healthcheck.StatusPassing)
	if got := len(p.Targets()); got != 2 {
		t.Fatalf("setup: expected 2 healthy targets, got %d", got)
	}

	sts[1].Set(healthcheck.StatusFailing)

	rr := newRR()
	rr.SetPool(p)
	const reqs = 50
	for range reqs {
		w := httptest.NewRecorder()
		rr.ServeHTTP(w, nil)
	}

	if got := hits2.Load(); got != 0 {
		t.Errorf("failing target received %d requests; expected 0", got)
	}
	if got := hits1.Load(); got != int64(reqs) {
		t.Errorf("healthy target received %d requests; expected %d", got, reqs)
	}
}
