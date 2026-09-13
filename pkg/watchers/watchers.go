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

// Package watchers defines the generic interface for change monitors that
// observe an external source (e.g. files on disk) and notify a consumer when
// its content changes. Implementations live in subpackages (e.g.
// watchers/filesystem); consumers supply what to watch and how to react.
package watchers

// Watcher is a monitor for out-of-band changes to an external source.
// Implementations are expected to be restartable: Start after Close begins
// watching again, and any changes that occurred while stopped are observed
// on the next start.
type Watcher interface {
	// Start begins (or resumes) watching. It is a no-op if the Watcher is
	// already running.
	Start()
	// Close stops watching and waits for the watch goroutine to exit. It is
	// a no-op if the Watcher is not running.
	Close()
}
