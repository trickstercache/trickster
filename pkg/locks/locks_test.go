/**
* Copyright 2018 Comcast Cable Communications Management, LLC
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

package locks

import (
	"sync"
	"testing"
	"time"
)

func TestLocks(t *testing.T) {

	var testVal = 0

	Acquire("test")
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		Acquire("test")
		testVal += 10
		Release("test")
		wg.Done()
	}()
	testVal++
	if testVal != 1 {
		t.Errorf("expected 1 got %d", testVal)
	}
	time.Sleep(time.Second * 1)
	Release("test")
	wg.Wait()

	if testVal != 11 {
		t.Errorf("expected 11 got %d", testVal)
	}

	// Cover Empty String Cases
	mtx := Acquire("")
	if mtx != nil {
		t.Errorf("expected nil got %v", mtx)
	}
	// Shouldn't matter but covers the code
	Release("")

}
