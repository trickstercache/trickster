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

package forwarding

import "strconv"

// CollapsedForwardingType enumerates the forwarding types
type CollapsedForwardingType int

const (
	// CFTypeBasic indicates a basic cache
	CFTypeBasic = CollapsedForwardingType(iota)
	// CFTypeProgressive indicates a progressive cache
	CFTypeProgressive
)

// CollapsedForwardingTypeNames is a map of forwarding types keyed by name
var CollapsedForwardingTypeNames = map[string]CollapsedForwardingType{
	"basic":       CFTypeBasic,
	"progressive": CFTypeProgressive,
}

// CollapsedForwardingTypeValues is a map of forwarding types keyed by internal id
var CollapsedForwardingTypeValues = make(map[CollapsedForwardingType]string)

func init() {
	for k, v := range CollapsedForwardingTypeNames {
		CollapsedForwardingTypeValues[v] = k
	}
}

func (t CollapsedForwardingType) String() string {
	if v, ok := CollapsedForwardingTypeValues[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}

// GetCollapsedForwardingType returns the CollapsedForwardingType for the provided name
// or CFTypeBasic if the name is invalid
func GetCollapsedForwardingType(name string) CollapsedForwardingType {
	if v, ok := CollapsedForwardingTypeNames[name]; ok {
		return v
	}
	return CFTypeBasic
}
