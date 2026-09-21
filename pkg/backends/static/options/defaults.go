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

import "time"

const (
	// DefaultDefaultFile is the file served when a directory is requested
	DefaultDefaultFile = "index.html"
	// DefaultMaxFileSizeBytes is the largest file held in the fileserver cache
	DefaultMaxFileSizeBytes = 1024 * 1024
	// DefaultMaxSizeBytes is the total size of the fileserver cache
	DefaultMaxSizeBytes = 128 * 1024 * 1024
	// DefaultMaxFiles is the most files held in the fileserver cache
	DefaultMaxFiles = 10000
	// DefaultRevalidationInterval is how often held files are compared to disk
	DefaultRevalidationInterval = 10 * time.Second
)
