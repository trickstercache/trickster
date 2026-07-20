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

package promql

import "strings"

// IsScalarCall reports whether query is a top-level call to PromQL's scalar
// conversion function. TSM uses this before fanout so range-query responses,
// which Prometheus encodes as matrices, retain scalar selection semantics.
func IsScalarCall(query string) bool {
	q := strings.TrimSpace(query)
	const name = "scalar"
	if len(q) <= len(name) || !strings.EqualFold(q[:len(name)], name) {
		return false
	}
	rest := strings.TrimSpace(q[len(name):])
	if len(rest) == 0 || rest[0] != '(' {
		return false
	}
	depth := 0
	var quote byte
	var escaped bool
	for i := range len(rest) {
		ch := rest[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && quote != '`' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(rest[i+1:]) == ""
			}
			if depth < 0 {
				return false
			}
		}
	}
	return false
}
