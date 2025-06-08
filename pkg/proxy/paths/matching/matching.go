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

package matching

import "strconv"

// PathMatchType enumerates the types of Path Matches used when registering Paths with the Router
type PathMatchType int

const (
	// PathMatchTypeExact indicates the router will map the Path by exact match against incoming requests
	PathMatchTypeExact = PathMatchType(iota)
	// PathMatchTypePrefix indicates the router will map the Path by prefix against incoming requests
	PathMatchTypePrefix

	PathMatchNameExact  = "exact"
	PathMatchNamePrefix = "prefix"
)

// Names is a map of PathMatchTypes keyed by string name
var Names = map[string]PathMatchType{
	PathMatchNameExact:  PathMatchTypeExact,
	PathMatchNamePrefix: PathMatchTypePrefix,
}

// Values is a map of PathMatchTypes valued by string name
var Values = make(map[PathMatchType]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
}

func (t PathMatchType) String() string {
	if v, ok := Values[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}
