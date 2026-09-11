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

package rr

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// SetPool writes h.pool while ServeHTTP reads it via h.pool.LiveTargets().
// Interface field assignment is two-word and not atomic, so config reload
// concurrent with in-flight requests should race.
func TestRoundRobinSetPoolRace(t *testing.T) {
	const cycles = 100

	initial, _, _ := albpool.New(-1, []http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
	h := &handler{}
	h.SetPool(initial)

	pools := make([]pool.Pool, 0, cycles+1)
	pools = append(pools, initial)
	var pmu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range cycles {
			p, _, _ := albpool.New(-1, []http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
			pmu.Lock()
			pools = append(pools, p)
			pmu.Unlock()
			h.SetPool(p)
		}
	}()

	go func() {
		defer wg.Done()
		for range cycles {
			h.ServeHTTP(httptest.NewRecorder(), nil)
		}
	}()

	wg.Wait()

	pmu.Lock()
	for _, p := range pools {
		p.Stop()
	}
	pmu.Unlock()
}
