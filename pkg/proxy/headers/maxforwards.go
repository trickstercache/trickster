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

package headers

import (
	"net/http"
	"strconv"
	"strings"
)

// ConsumeMaxForwards applies the Max-Forwards field of a TRACE or OPTIONS
// request per RFC 9110 7.6.2: a zero count makes this proxy the final
// recipient, and any other count is decremented before the request is
// forwarded. It reports whether the caller must answer the request itself.
func ConsumeMaxForwards(r *http.Request) bool {
	if r == nil || r.Header == nil ||
		(r.Method != http.MethodOptions && r.Method != http.MethodTrace) {
		return false
	}
	v := strings.TrimSpace(r.Header.Get(NameMaxForwards))
	if v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		// an unparsable count is not a hop budget this proxy can honor;
		// forward the request unchanged and let the origin decide
		return false
	}
	if n == 0 {
		return true
	}
	r.Header.Set(NameMaxForwards, strconv.Itoa(n-1))
	return false
}
