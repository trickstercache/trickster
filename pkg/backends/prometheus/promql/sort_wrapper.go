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

// SortWrapper describes outer PromQL sort functions that TSM must apply after
// merging the wrapped expression across backends.
type SortWrapper struct {
	InnerQuery string
	Descending bool
}

// ParseSortWrapper unwraps an outer sort or sort_desc function. If multiple
// sort wrappers are nested, InnerQuery excludes all of them and Descending
// reflects the outermost wrapper that determines the final ordering.
func ParseSortWrapper(query string) (SortWrapper, bool) {
	q := strings.TrimSpace(query)
	inner, descending, ok := unwrapSortFunction(q)
	if !ok || strings.TrimSpace(inner) == "" {
		return SortWrapper{}, false
	}
	for {
		next, _, found := unwrapSortFunction(inner)
		if !found || strings.TrimSpace(next) == "" {
			break
		}
		inner = next
	}
	return SortWrapper{
		InnerQuery: strings.TrimSpace(inner),
		Descending: descending,
	}, true
}

func unwrapSortFunction(query string) (inner string, descending bool, ok bool) {
	if inner, ok := unwrapUnaryFunction(query, "sort_desc"); ok {
		return inner, true, true
	}
	if inner, ok := unwrapUnaryFunction(query, "sort"); ok {
		return inner, false, true
	}
	return "", false, false
}
