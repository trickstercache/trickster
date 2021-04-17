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

package engines

import "strconv"

// RevalidationStatus enumerates the possible statuses for cache revalidation
type RevalidationStatus int

const (
	// RevalStatusNone indicates the object will not undergo revalidation against the origin
	RevalStatusNone = RevalidationStatus(iota)
	// RevalStatusInProgress indicates the object is currently being revalidated against the origin
	RevalStatusInProgress
	// RevalStatusLocal is used during a cache Range Miss, and indicates that while the user-requested ranges
	// for an object are uncached and must be fetched from the origin, other ranges for the object are cached
	// but require revalidation. When a request is in this state, Trickster will use the response headers from
	// the range miss to locally revalidate the cached content instead of making a separate request to the
	// origin for revalidating the cached ranges.
	RevalStatusLocal
	// RevalStatusOK indicates the object was successfully revalidated against the origin and is still fresh
	RevalStatusOK
	// RevalStatusFailed indicates the origin returned a new object for the URL to replace the cached version
	RevalStatusFailed
)

var revalidationStatusNames = map[string]RevalidationStatus{
	"none":         RevalStatusNone,
	"revalidating": RevalStatusInProgress,
	"revalidated":  RevalStatusOK,
	"failed":       RevalStatusFailed,
	"local":        RevalStatusLocal,
}

var revalidationStatusValues = map[RevalidationStatus]string{
	RevalStatusNone:       "none",
	RevalStatusInProgress: "revalidating",
	RevalStatusOK:         "revalidated",
	RevalStatusFailed:     "failed",
	RevalStatusLocal:      "local",
}

func (s RevalidationStatus) String() string {
	if v, ok := revalidationStatusValues[s]; ok {
		return v
	}
	return strconv.Itoa(int(s))
}
