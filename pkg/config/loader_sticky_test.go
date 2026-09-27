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
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const stickyConfig = `
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
      pool: [a, b]
      sticky: STICKY
`

// stickyKey is long enough to be accepted, and distinctive enough to find in any output
const stickyKey = "sticky-key-that-must-never-be-shown"

func loadStickyConfig(t *testing.T, sticky string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	yml := strings.Replace(stickyConfig, "STICKY", sticky, 1)
	require.NoError(t, os.WriteFile(path, []byte(yml), 0o600))
	c, err := Load([]string{"-config", path})
	require.NoError(t, err)
	return c
}

func TestStickySecretIsInNeitherConfigView(t *testing.T) {
	c := loadStickyConfig(t, "{mode: cookie, secret: "+stickyKey+"}")
	require.Equal(t, stickyKey, string(c.Backends["lb"].ALBOptions.Sticky.Secret), "the key itself is kept")
	for name, output := range map[string]string{
		"String":          c.String(),
		"SanitizedString": c.SanitizedString(),
	} {
		require.NotContains(t, output, stickyKey, name)
		require.Contains(t, output, "mode: cookie", "%s shows the rest of the block", name)
	}
}
