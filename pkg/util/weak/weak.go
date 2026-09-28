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

// Package weak separates the application's non-cryptographic randomness, in
// package compat, from the reproducible randomness tests use, in package weaktest.
package weak

import (
	"os"
	"sync/atomic"
)

// TestModeEnv names the environment variable that tests set, to any nonempty
// value, when they launch a trickster process that should stay in test mode.
const TestModeEnv = "TRICKSTER_TEST_MODE"

var nonTestUsage atomic.Bool

// RegisterNonTestUsage marks the process as the application rather than a test,
// after which package weaktest panics when used.
func RegisterNonTestUsage() {
	nonTestUsage.Store(true)
}

// RegisterUnlessTestMode calls RegisterNonTestUsage unless TestModeEnv is set.
// The application's main calls it first.
func RegisterUnlessTestMode() {
	if os.Getenv(TestModeEnv) == "" {
		RegisterNonTestUsage()
	}
}

// AssertTestUsage panics if RegisterNonTestUsage has been called. Test-only
// randomness calls it so that the application cannot use it.
func AssertTestUsage() {
	if nonTestUsage.Load() {
		panic("weak: test-only randomness used outside of a test; use package compat")
	}
}
