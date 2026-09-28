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

// Package weaktest provides non-cryptographic randomness for tests and test
// helpers. Every function panics once the application has registered at startup.
package weaktest

import (
	"math/rand/v2"

	"github.com/trickstercache/trickster/v2/pkg/util/weak"
)

// Rand is a pseudo-random number generator returned by NewRand.
type Rand = rand.Rand

// NewRand returns a Rand whose sequence is fixed by seed1 and seed2, so test data
// and shuffles are reproducible.
func NewRand(seed1, seed2 uint64) *Rand {
	weak.AssertTestUsage()
	return rand.New(rand.NewPCG(seed1, seed2)) // #nosec G404 -- test-only; weak.AssertTestUsage keeps it out of the application
}

// IntN returns a random int in [0, n) from a randomly seeded source. It panics
// if n <= 0.
func IntN(n int) int {
	weak.AssertTestUsage()
	return rand.IntN(n) // #nosec G404 -- test-only; weak.AssertTestUsage keeps it out of the application
}
