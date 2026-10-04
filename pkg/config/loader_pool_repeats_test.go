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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"

	"github.com/stretchr/testify/require"
)

const repeatedPoolConfig = `
backends:
  a:
    provider: reverseproxycache
    origin_url: http://127.0.0.1:9001
  b:
    provider: reverseproxycache
    origin_url: http://127.0.0.1:9002
  lb:
    provider: alb
    alb:
      mechanism: rr
      pool: POOL
`

func loadPoolConfig(t *testing.T, pool string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	yml := strings.Replace(repeatedPoolConfig, "POOL", pool, 1)
	require.NoError(t, os.WriteFile(path, []byte(yml), 0o600))
	return Load([]string{"-config", path})
}

func TestLoadDedupesRepeatedPoolMembers(t *testing.T) {
	c, err := loadPoolConfig(t, "[a, a, b]")
	require.NoError(t, err)
	require.Equal(t, ao.Members("a", "b"), c.Backends["lb"].ALBOptions.Pool)
	var warnings []string
	for _, w := range c.LoaderWarnings {
		if strings.Contains(w, "repeating a pool member") {
			warnings = append(warnings, w)
		}
	}
	require.Len(t, warnings, 1, "one warning per alb")
	require.Contains(t, warnings[0], `alb "lb"`)
	require.Contains(t, warnings[0], "{name: a, weight: 2}")

	c, err = loadPoolConfig(t, "[{name: a, weight: 2}, b]")
	require.NoError(t, err)
	for _, w := range c.LoaderWarnings {
		require.NotContains(t, w, "repeating a pool member")
	}

	_, err = loadPoolConfig(t, "[{name: a, weight: 2}, b, {name: a, weight: 3}]")
	require.ErrorIs(t, err, ao.ErrConflictingPoolWeights)
}
