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

package pool

import "strconv"

// Mechanism defines the load balancing mechanism identifier type
type Mechanism byte

const (
	// RoundRobin defines the Basic Round Robin load balancing mechanism
	RoundRobin Mechanism = iota
	// FirstResponse defines the First Response load balancing mechanism
	FirstResponse
	// FirstGoodResponse defines the First Good Response load balancing mechanism
	FirstGoodResponse
	// NewestLastModified defines the Newest Last-Modified load balancing mechanism
	NewestLastModified
	// TimeSeriesMerge defines the Time Series Merge load balancing mechanism
	TimeSeriesMerge
)

// MechanismLookup provides for looking up Mechanisms by name
var MechanismLookup = map[string]Mechanism{
	"rr":  RoundRobin,
	"fr":  FirstResponse,
	"fgr": FirstGoodResponse,
	"nlm": NewestLastModified,
	"tsm": TimeSeriesMerge,
}

// MechanismValues provides for looking up Mechanism by names
var MechanismValues = map[Mechanism]string{}

// GetMechanismByName returns the Mechanism value and True if the mechanism name is known
func GetMechanismByName(name string) (Mechanism, bool) {
	m, ok := MechanismLookup[name]
	return m, ok
}

func init() {
	for k, v := range MechanismLookup {
		MechanismValues[v] = k
	}
}

func (m Mechanism) String() string {
	if v, ok := MechanismValues[m]; ok {
		return v
	}
	return strconv.Itoa(int(m))
}
