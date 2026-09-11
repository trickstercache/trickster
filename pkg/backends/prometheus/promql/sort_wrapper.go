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

import "github.com/prometheus/prometheus/promql/parser"

// SortWrapper describes outer PromQL sort functions that TSM must apply after
// merging the wrapped expression across backends.
type SortWrapper struct {
	Inner      Expr
	Descending bool
}

// ParseSortWrapper unwraps an outer sort or sort_desc call. If multiple sort
// wrappers are nested, Inner excludes all of them and Descending reflects the
// outermost wrapper that determines the final ordering.
func ParseSortWrapper(e Expr) (SortWrapper, bool) {
	inner, descending, ok := unwrapSortFunction(e)
	if !ok {
		return SortWrapper{}, false
	}
	for {
		next, _, found := unwrapSortFunction(inner)
		if !found {
			break
		}
		inner = next
	}
	return SortWrapper{Inner: inner, Descending: descending}, true
}

func unwrapSortFunction(e Expr) (inner Expr, descending bool, ok bool) {
	call, isCall := e.node.(*parser.Call)
	if !isCall || call.Func == nil || len(call.Args) != 1 {
		return Expr{}, false, false
	}
	switch call.Func.Name {
	case functionSort:
		return e.child(call.Args[0]), false, true
	case functionSortDesc:
		return e.child(call.Args[0]), true, true
	default:
		return Expr{}, false, false
	}
}
