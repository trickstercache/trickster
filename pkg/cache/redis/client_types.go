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

package redis

import "strconv"

// clientType enumerates the supported Redis client types
type clientType int

const (
	clientTypeStandard = clientType(iota)
	clientTypeCluster
	clientTypeSentinel
)

var clientTypeNames = map[string]clientType{
	"standard": clientTypeStandard,
	"cluster":  clientTypeCluster,
	"sentinel": clientTypeSentinel,
}

var clientTypeValues = map[clientType]string{}

func init() {
	// create inverse lookup map
	for k, v := range clientTypeNames {
		clientTypeValues[v] = k
	}
}

func (t clientType) String() string {
	if v, ok := clientTypeValues[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}
