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

package weak_test

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/weak"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/compat"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func TestRegisterUnlessTestMode(t *testing.T) {
	t.Cleanup(weak.ResetNonTestUsage)
	require.NotPanics(t, weak.AssertTestUsage)

	t.Setenv(weak.TestModeEnv, "1")
	weak.RegisterUnlessTestMode()
	require.NotPanics(t, weak.AssertTestUsage)

	t.Setenv(weak.TestModeEnv, "")
	weak.RegisterUnlessTestMode()
	require.Panics(t, weak.AssertTestUsage)
}

func TestTestOnlyRandomnessPanicsInApplication(t *testing.T) {
	t.Cleanup(weak.ResetNonTestUsage)
	require.NotPanics(t, func() { weaktest.NewRand(1, 2).Uint64() })
	require.NotPanics(t, func() { weaktest.IntN(2) })

	weak.RegisterNonTestUsage()
	require.PanicsWithValue(t, "weak: test-only randomness used outside of a test; use package compat",
		func() { weaktest.NewRand(1, 2) })
	require.Panics(t, func() { weaktest.IntN(2) })
	require.NotPanics(t, func() {
		compat.Uint64()
		compat.IntN(2)
		compat.Int64()
	})
}
