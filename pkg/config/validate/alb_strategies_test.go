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

package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/types"

	"github.com/stretchr/testify/require"
)

const albStrategyConfig = `
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
      pool: [a, {name: b, weight: 3}]
ALB
`

func validateALB(t *testing.T, alb string) error {
	t.Helper()
	indented := "      " + strings.ReplaceAll(strings.TrimSpace(alb), "\n", "\n      ")
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	yml := strings.Replace(albStrategyConfig, "ALB", indented, 1)
	require.NoError(t, os.WriteFile(path, []byte(yml), 0o600))
	c, err := config.Load([]string{"-config", path})
	if err != nil {
		return err
	}
	return Validate(c)
}

func TestALBStrategyConfigs(t *testing.T) {
	for name, alb := range map[string]string{
		"p2c":         "mechanism: p2c",
		"lc":          "mechanism: least_connections",
		"hrw":         "mechanism: hrw",
		"hrw keyed":   "mechanism: hrw\nhrw:\n  key: header:X-Tenant\n  ipv6_prefix: 56",
		"lt":          "mechanism: lt",
		"lt tuned":    "mechanism: lt\nlt:\n  status_codes: [{start: 200, end: 499}]\n  decay: 30s\n  signal: first_write",
		"fgr codes":   "mechanism: fgr\nfgr:\n  status_codes: [200, 204]",
		"fgr ranges":  "mechanism: fgr\nfgr:\n  status_codes: [{start: 200, end: 299}, 304]",
		"rr as today": "mechanism: rr",
	} {
		require.NoError(t, validateALB(t, alb), name)
	}
	for name, test := range map[string]struct {
		alb  string
		want error
	}{
		"hrw block on rr":  {"mechanism: rr\nhrw:\n  key: host", ao.ErrHRWOnlyForHRW},
		"lt block on p2c":  {"mechanism: p2c\nlt:\n  decay: 5s", ao.ErrLTOnlyForLT},
		"bad key source":   {"mechanism: hrw\nhrw:\n  key: port", ao.ErrInvalidKeySource},
		"bad ipv6 prefix":  {"mechanism: hrw\nhrw:\n  ipv6_prefix: 200", ao.ErrInvalidIPv6Prefix},
		"stream lt signal": {"mechanism: lt\nlt:\n  signal: connect", ao.ErrInvalidLTSignal},
		"bad lt range":     {"mechanism: lt\nlt:\n  status_codes: [{start: 100, end: 900}]", types.ErrInvalidStatusRange},
		"bad fgr range":    {"mechanism: fgr\nfgr:\n  status_codes: [42]", types.ErrInvalidStatusRange},
		"output_format":    {"mechanism: rr\noutput_format: prometheus", ao.ErrOutputFormatOnlyForTSM},
	} {
		require.ErrorIs(t, validateALB(t, test.alb), test.want, name)
	}
}
