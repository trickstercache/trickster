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

package config

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

// CacheTypeNames is a map of cache types keyed by name
var CacheTypeNames = map[string]CacheType{
	"memory":     CacheTypeMemory,
	"filesystem": CacheTypeFilesystem,
	"redis":      CacheTypeRedis,
	"bbolt":      CacheTypeBbolt,
	"badger":     CacheTypeBadgerDB,
}

// CacheTypeValues is a map of cache types keyed by internal id
var CacheTypeValues = map[CacheType]string{
	CacheTypeMemory:     "memory",
	CacheTypeFilesystem: "filesystem",
	CacheTypeRedis:      "redis",
	CacheTypeBbolt:      "bbolt",
	CacheTypeBadgerDB:   "badger",
}

func (t CacheType) String() string {
	if v, ok := CacheTypeValues[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}
