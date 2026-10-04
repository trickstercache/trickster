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

// Package compat collects the application's non-cryptographic randomness in one
// place, so each use can be reviewed and replaced when it is no longer suitable.
package compat

import "math/rand/v2"

// Uint64 returns a random uint64 from a randomly seeded source; it must not be
// used for secrets.
func Uint64() uint64 {
	return rand.Uint64() // #nosec G404 -- callers use it for identifiers and spreading, not secrets
}

// IntN returns a random int in [0, n) from a randomly seeded source; it must not
// be used for secrets. It panics if n <= 0.
func IntN(n int) int {
	return rand.IntN(n) // #nosec G404 -- callers use it for sampling, not secrets
}

// Int64 returns a random non-negative int64 from a randomly seeded source; it
// must not be used for secrets.
func Int64() int64 {
	return rand.Int64() // #nosec G404 -- callers use it for jitter, not secrets
}
