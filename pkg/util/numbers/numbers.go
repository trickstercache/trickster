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

package numbers

import (
	"math"
)

// SafeAdd returns (a + b, true) if the sum value wouldn't overflow MaxInt.
// Otherwise (a, false) is returned.
func SafeAdd(a, b int) (int, bool) {
	if (b > 0 && a > math.MaxInt-b) || (b < 0 && a < math.MinInt-b) {
		return a, false // overflow would occur
	}
	return a + b, true
}

// SafeAdd64 returns (a + b, true) if the sum value wouldn't overflow MaxInt64.
// Otherwise (a, false) are returned.
func SafeAdd64(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return a, false // overflow would occur
	}
	return a + b, true
}

// IsStringUint returns true if all characters in the string are integers
// without checking for MaxInt overflow
func IsStringUint(input string) bool {
	for _, c := range input {
		if c < 48 || c > 57 {
			return false
		}
	}
	return true
}
