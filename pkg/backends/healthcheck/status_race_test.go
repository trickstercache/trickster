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
	"sync"
	"testing"
	"time"
)

// failingSince is written by target.notifyStatus on every status transition
// (probe goroutine) and read by FailingSince()/String() from the status-page
// builder goroutine. Both paths run concurrently in production. Today the
// write is unprotected; the read returns the field directly. Fail under
// -race; latent corruption otherwise.
func TestStatusFailingSinceConcurrent(t *testing.T) {
	s := &Status{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1000 {
			s.SetFailingSince(time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			_ = s.FailingSince()
		}
	}()
	wg.Wait()
}
