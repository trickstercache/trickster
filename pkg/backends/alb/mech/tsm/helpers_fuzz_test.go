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

package tsm

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

// Invariant: when any shard returned a 2xx status with a non-nil mergeFunc,
// pickWinner must return a 2xx winner. The fuzzer shuffles status codes and
// nil-mergeFuncs across N slots; the fallback path must never be taken if a
// 2xx slot exists.
func FuzzPickWinnerInvariant(f *testing.F) {
	f.Add(byte(0), byte(2), byte(5))
	f.Add(byte(2), byte(0), byte(5))
	f.Add(byte(5), byte(0), byte(2))
	f.Add(byte(0), byte(0), byte(0))

	f.Fuzz(func(t *testing.T, a, b, c byte) {
		codeFor := func(b byte) int {
			switch b % 7 {
			case 0:
				return 0 // no response
			case 1:
				return 100
			case 2:
				return 200
			case 3:
				return 204
			case 4:
				return 302
			case 5:
				return 404
			default:
				return 502
			}
		}

		noopFunc := func(_ http.ResponseWriter, _ *http.Request, _ *merge.Accumulator, _ int) {}

		results := []gatherResult{
			{statusCode: codeFor(a), mergeFunc: noopFunc},
			{statusCode: codeFor(b), mergeFunc: noopFunc},
			{statusCode: codeFor(c), mergeFunc: noopFunc},
		}

		// Nil out the mergeFunc on slots with statusCode 0 to mirror reality
		// (no response means no respondFunc captured).
		for i := range results {
			if results[i].statusCode == 0 {
				results[i].mergeFunc = nil
			}
		}

		hasAny2xx := false
		for _, r := range results {
			if r.mergeFunc != nil && r.statusCode >= 200 && r.statusCode < 300 {
				hasAny2xx = true
				break
			}
		}
		hasAnyFunc := false
		for _, r := range results {
			if r.mergeFunc != nil {
				hasAnyFunc = true
				break
			}
		}

		mrf, _ := pickWinner(results)
		switch {
		case hasAny2xx && mrf == nil:
			t.Errorf("had a 2xx but pickWinner returned nil: %+v", results)
		case !hasAnyFunc && mrf != nil:
			t.Errorf("no mergeFunc available but pickWinner returned non-nil: %+v", results)
		}
	})
}

// Invariant: aggregateStatus returns has2xx XOR (status >= 300 || status == 0).
// In other words, when has2xx is true the returned status MUST be in [200,299].
// When has2xx is false, status MUST NOT be in [200,299].
func FuzzAggregateStatusInvariant(f *testing.F) {
	f.Add(uint16(200), uint16(500))
	f.Add(uint16(502), uint16(400))
	f.Add(uint16(206), uint16(204))
	f.Add(uint16(0), uint16(0))

	f.Fuzz(func(t *testing.T, a, b uint16) {
		toCode := func(v uint16) int {
			if v == 0 {
				return 0
			}
			return int(100 + v%500)
		}
		results := []gatherResult{
			{statusCode: toCode(a)},
			{statusCode: toCode(b)},
		}
		code, _, has2xx, _ := aggregateStatus(results)
		if has2xx {
			if code < 200 || code >= 300 {
				t.Errorf("has2xx=true but code=%d is not 2xx; %+v", code, results)
			}
		} else if code >= 200 && code < 300 {
			t.Errorf("has2xx=false but code=%d is 2xx; %+v", code, results)
		}
	})
}
