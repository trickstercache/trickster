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

// Package status governs the possible Cache Lookup Status values
package status

import "strconv"

// LookupStatus defines the possible status of a cache lookup
type LookupStatus int

const (
	// LookupStatusHit indicates a full cache hit on lookup
	LookupStatusHit = LookupStatus(iota)
	// LookupStatusPartialHit indicates a partial cache hit (key exists and has some data
	// for requested time range, but not all) on lookup
	LookupStatusPartialHit
	// LookupStatusRangeMiss indicates a range miss (key exists but no data for requested time range) on lookup
	LookupStatusRangeMiss
	// LookupStatusKeyMiss indicates a full key miss (cache key does not exist) on lookup
	LookupStatusKeyMiss
	// LookupStatusRevalidated indicates the cached object exceeded the freshness lifetime but
	// was revalidated against the upstream server and is treated as a cache hit
	LookupStatusRevalidated
	// LookupStatusPurge indicates the cache key, if it existed, was purged as directed
	// in upstream response or down stream request http headers
	LookupStatusPurge
	// LookupStatusProxyError indicates that a proxy error occurred retrieving a cacheable dataset
	// in upstream response or down stream request http headers
	LookupStatusProxyError
	// LookupStatusProxyOnly indicates that the request was fully proxied to the origin without using the cache
	LookupStatusProxyOnly
	// LookupStatusNegativeCacheHit indicates that the request was served as a hit from the Negative Response Cache
	LookupStatusNegativeCacheHit
	// LookupStatusError indicates that there was an error looking up the object in the cache
	LookupStatusError
	// LookupStatusProxyHit indicates that the request joined an existing proxy download of the same object
	LookupStatusProxyHit
)

var cacheLookupStatusNames = map[string]LookupStatus{
	"hit":         LookupStatusHit,
	"phit":        LookupStatusPartialHit,
	"rhit":        LookupStatusRevalidated,
	"rmiss":       LookupStatusRangeMiss,
	"kmiss":       LookupStatusKeyMiss,
	"purge":       LookupStatusPurge,
	"proxy-error": LookupStatusProxyError,
	"proxy-only":  LookupStatusProxyOnly,
	"nchit":       LookupStatusNegativeCacheHit,
	"proxy-hit":   LookupStatusProxyHit,
	"error":       LookupStatusError,
}

var cacheLookupStatusValues = map[LookupStatus]string{
	LookupStatusHit:              "hit",
	LookupStatusPartialHit:       "phit",
	LookupStatusRevalidated:      "rhit",
	LookupStatusRangeMiss:        "rmiss",
	LookupStatusKeyMiss:          "kmiss",
	LookupStatusPurge:            "purge",
	LookupStatusProxyError:       "proxy-error",
	LookupStatusProxyOnly:        "proxy-only",
	LookupStatusNegativeCacheHit: "nchit",
	LookupStatusProxyHit:         "proxy-hit",
	LookupStatusError:            "error",
}

func (s LookupStatus) String() string {
	if v, ok := cacheLookupStatusValues[s]; ok {
		return v
	}
	return strconv.Itoa(int(s))
}
