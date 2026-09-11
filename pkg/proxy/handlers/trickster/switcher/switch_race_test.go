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

package switcher

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSwitchHandlerConcurrentUpdateReadIsRaceFree(t *testing.T) {
	t.Parallel()

	var served atomic.Int64
	makeMux := func() http.Handler {
		m := http.NewServeMux()
		m.HandleFunc("/", func(_ http.ResponseWriter, _ *http.Request) {
			served.Add(1)
		})
		return m
	}

	sh := NewSwitchHandler(makeMux())

	var stop atomic.Bool
	var wg sync.WaitGroup

	const writers, readers = 8, 8

	for range writers {
		wg.Go(func() {
			for !stop.Load() {
				sh.Update(makeMux())
			}
		})
	}

	for range readers {
		wg.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for !stop.Load() {
				w := httptest.NewRecorder()
				sh.ServeHTTP(w, r)
			}
		})
	}

	time.Sleep(500 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	if served.Load() == 0 {
		t.Fatal("no requests served; readers never reached a non-nil handler")
	}
}
