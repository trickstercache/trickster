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

package options

const (
	// DefaultCacheIndexReap is the default Cache Index Reap interval (in milliseconds)
	DefaultCacheIndexReap = 3000
	// DefaultCacheIndexFlush is the default Cache Index Flush interval (in milliseconds)
	DefaultCacheIndexFlush = 5000
	// DefaultCacheMaxSizeBytes is the default Max Cache Size in Bytes
	DefaultCacheMaxSizeBytes = 536870912
	// DefaultMaxSizeBackoffBytes is the default Max Cache Backoff Size in Bytes
	DefaultMaxSizeBackoffBytes = 16777216
	// DefaultMaxSizeObjects is the default Max Cache Object Count
	DefaultMaxSizeObjects = 0
	// DefaultMaxSizeBackoffObjects is the default Max Cache Backoff Object Count
	DefaultMaxSizeBackoffObjects = 100
)
