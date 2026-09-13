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

package rule

import (
	"strconv"
	"sync"
	"testing"
)

// a rule evaluating a regular expression must not share mutable state with
// rules being parsed by a concurrent reload
func TestRMatchEvaluatesWhileRulesReload(t *testing.T) {
	r := benchRule(t, benchOpts("header", testRuleHeader, "string", "rmatch", "^trick.*$"))
	hr := benchRequest()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				if h, _, _ := r.EvaluateOpArg(hr); h == nil {
					t.Error("expected a handler")
					return
				}
			}
		}
	})
	for i := range 200 {
		benchRule(t, benchOpts("header", testRuleHeader, "string", "rmatch",
			"^trick"+strconv.Itoa(i)+".*$"))
	}
	close(stop)
	wg.Wait()
}
