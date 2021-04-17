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

package providers

import "strconv"

// Provider enumerates the cache providers
type Provider int

const (
	// Memory indicates a memory cache
	Memory = Provider(iota)
	// Filesystem indicates a filesystem cache
	Filesystem
	// Redis indicates a Redis cache
	Redis
	// Bbolt indicates a Bbolt cache
	Bbolt
	// BadgerDB indicates a BadgerDB cache
	BadgerDB
)

// Names is a map of cache providers keyed by name
var Names = map[string]Provider{
	"memory":     Memory,
	"filesystem": Filesystem,
	"redis":      Redis,
	"bbolt":      Bbolt,
	"badger":     BadgerDB,
}

// Values is a map of cache providers keyed by internal id
var Values = make(map[Provider]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
}

func (p Provider) String() string {
	if v, ok := Values[p]; ok {
		return v
	}
	return strconv.Itoa(int(p))
}
