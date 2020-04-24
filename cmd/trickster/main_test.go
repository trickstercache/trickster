/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package main

import (
	"sync"
	"testing"
)

func TestMain(t *testing.T) {
	fatalStartupErrors = false
	main()
	// Successful test criteria is that the call to main returns without timing out on wg.Wait()
}

func TestRunConfig(t *testing.T) {
	wg := &sync.WaitGroup{}
	runConfig(nil, wg, nil, nil, []string{}, false)

	runConfig(nil, wg, nil, nil, []string{"-version"}, false)

	runConfig(nil, wg, nil, nil, []string{"-origin-type", "rpc", "-origin-url", "http://tricksterproxy.io"}, false)

}
