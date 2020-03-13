/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package types

import "strconv"

// CacheType enumerates the methodologies for maintaining time series cache data
type CacheType int

const (
	// CacheTypeMemory indicates a memory cache
	CacheTypeMemory = CacheType(iota)
	// CacheTypeFilesystem indicates a filesystem cache
	CacheTypeFilesystem
	// CacheTypeRedis indicates a Redis cache
	CacheTypeRedis
	// CacheTypeBbolt indicates a Bbolt cache
	CacheTypeBbolt
	// CacheTypeBadgerDB indicates a BadgerDB cache
	CacheTypeBadgerDB
)

// Names is a map of cache types keyed by name
var Names = map[string]CacheType{
	"memory":     CacheTypeMemory,
	"filesystem": CacheTypeFilesystem,
	"redis":      CacheTypeRedis,
	"bbolt":      CacheTypeBbolt,
	"badger":     CacheTypeBadgerDB,
}

// Values is a map of cache types keyed by internal id
var Values = make(map[CacheType]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
}

func (t CacheType) String() string {
	if v, ok := Values[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}
