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

package ratelimit

import "time"

// monoBase is a monotonic anchor. time.Since uses the monotonic clock, so a wall-clock step
// cannot roll a bucket backwards. Tests pass their own timestamps and do not read this.
var monoBase = time.Now()

// Now is the process-local monotonic time in nanoseconds, for bucket arithmetic and key expiry.
func Now() int64 {
	return int64(time.Since(monoBase))
}
