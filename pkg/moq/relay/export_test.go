/*
 * Copyright 2026 The Trickster Authors
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

package relay

import "github.com/trickstercache/trickster/v2/pkg/moq/message"

// GroupEnded reports whether the relay holds group of the named track and knows where it ends
func GroupEnded(r *Relay, scope Scope, name message.FullTrackName, group uint64) bool {
	t := r.tracks.acquire(r, scope, name)
	defer r.tracks.release(t)
	_, known := t.lastObject(group)
	return known
}
