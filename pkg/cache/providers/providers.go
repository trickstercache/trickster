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
	// MemoryID indicates a memory cache
	MemoryID = Provider(iota)
	// FilesystemID indicates a filesystem cache
	FilesystemID
	// RedisID indicates a Redis cache
	RedisID
	// BBoltID indicates a BBolt cache
	BBoltID
	// BadgerDBID indicates a BadgerDB cache
	BadgerDBID

	Memory     = "memory"
	Filesystem = "filesystem"
	Redis      = "redis"
	BBolt      = "bbolt"
	BadgerDB   = "badger"
)

// Names is a map of cache providers keyed by name
var Names = map[string]Provider{
	Memory:     MemoryID,
	Filesystem: FilesystemID,
	Redis:      RedisID,
	BBolt:      BBoltID,
	BadgerDB:   BadgerDBID,
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

// UsesIndex returns true if the providerName uses an index
// providerName is expected to already be lowercase/no-space
func UsesIndex(providerName string) bool {
	return providerName != BadgerDB && providerName != Redis && providerName != Memory
}
