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

package types

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvString(t *testing.T) {
	os.Setenv("FOO", "bar")
	os.Setenv("BAR", "baz")
	example := EnvString("")
	// expect bar
	err := example.Unmarshal([]byte("${FOO}"))
	require.NoError(t, err)
	require.Equal(t, "bar", string(example))
	// expect baz
	err = example.Unmarshal([]byte("${BAR}"))
	require.NoError(t, err)
	require.Equal(t, "baz", string(example))
	// expect both
	err = example.Unmarshal([]byte("${FOO}${BAR}"))
	require.NoError(t, err)
	require.Equal(t, "barbaz", string(example))
}

func TestEnvStringMap(t *testing.T) {
	os.Setenv("FIZZ", "buzz")
	os.Setenv("BIZZ", "quux")
	example := EnvStringMap{}
	// expect fizz
	err := example.Unmarshal([]byte(`abc: "${FIZZ}"`))
	require.NoError(t, err)
	require.Equal(t, "buzz", example["abc"])
	// expect bizz
	err = example.Unmarshal([]byte(`def: "${BIZZ}"`))
	require.NoError(t, err)
	require.Equal(t, "quux", example["def"])
	// expect both
	err = example.Unmarshal([]byte(`abc: "${FIZZ}"` + "\n" + `def: "${BIZZ}"`))
	require.NoError(t, err)
	require.Equal(t, "buzz", example["abc"])
	require.Equal(t, "quux", example["def"])
}
