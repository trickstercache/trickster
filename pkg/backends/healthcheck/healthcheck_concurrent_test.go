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

package healthcheck

import (
	"strconv"
	"sync"
	"testing"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// healthChecker.targets, .statuses, and .subscribers are all read/written
// without synchronization. Statuses() returns the live map. Concurrent
// Register/Unregister/Statuses access should fail under -race.
func TestHealthCheckerConcurrentRegisterAndStatuses(t *testing.T) {
	logger.SetLogger(testLogger)
	hc := New()
	const iters = 1000
	names := []string{"a", "b", "c", "d"}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range iters {
			n := names[i%len(names)]
			// Interval: 0 keeps the probe loop unstarted.
			_, _ = hc.Register(n, "desc-"+strconv.Itoa(i), &ho.Options{}, nil)
			if i%3 == 0 {
				hc.Unregister(n)
			}
		}
	}()

	go func() {
		defer wg.Done()
		for range iters {
			s := hc.Statuses()
			for _, v := range s {
				if v != nil {
					_ = v.Get()
				}
			}
		}
	}()

	wg.Wait()
}
