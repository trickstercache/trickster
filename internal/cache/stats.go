/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package cache

import "time"

// Metadata maintains metadata about a Cache where Retention enforcement is managed internally,
// like memory or bbolt. It is not used for independently managed caches like Redis.
type Metadata struct {
	// CacheSize represents the size of the cache in bytes
	CacheSize int
	// ObjectCount represents the count of objects in the Cache
	ObjectCount int
	// Objects is a map of Objects in the Cache
	Objects map[string]Object
}

// Object contains metadataa about an item in the Cache
type Object struct {
	// Size the size of the Object in bytes
	Size int
	// LastWrite is the time the object was last Written
	LastWrite time.Time
	// LastAccess is the time the object was last Accessed
	LastAccess time.Time
}
