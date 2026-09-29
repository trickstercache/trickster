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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const stepAlignmentPoolConfig = `
backends:
  prom-truncate:
    provider: prometheus
    origin_url: http://127.0.0.1:9090
    step_alignment: truncate
  prom-off:
    provider: prometheus
    origin_url: http://127.0.0.1:9091
    step_alignment: "off"
  merged:
    provider: alb
    alb: {mechanism: tsm, output_format: prometheus, pool: [{name: prom-truncate}, {name: prom-off}]}
EXTRA
`

func stepAlignmentNotes(c *config.Config) []string {
	var out []string
	for _, w := range c.LoaderWarnings {
		if strings.Contains(w, "step_alignment") || strings.Contains(w, "step alignment") {
			out = append(out, w)
		}
	}
	return out
}

func TestRoutesRulesAndPoolsChecksALBStepAlignment(t *testing.T) {
	for name, test := range map[string]struct {
		extra, want string
	}{
		"a merge follows its leader": {"", ""},
		"a mode a member can't apply": {
			"  strict:\n    provider: alb\n    step_alignment: partial\n" +
				"    alb: {mechanism: rr, pool: [{name: prom-truncate}]}",
			`unsupported step_alignment "partial" for alb "strict": pool members [prom-truncate] can't apply it`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trickster.yaml")
			doc := strings.ReplaceAll(stepAlignmentPoolConfig, "EXTRA\n", test.extra+"\n")
			require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
			c, err := config.Load([]string{"-config", path})
			require.NoError(t, err)
			require.NoError(t, Validate(c))
			require.NoError(t, c.Process())
			err = RoutesRulesAndPools(c, make(backends.Backends, len(c.Backends)))
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []string{`alb "merged" pool members apply different step alignment modes ` +
				`(prom-truncate=truncate, prom-off=off); the alb has all of them apply truncate`},
				stepAlignmentNotes(c))
		})
	}
}

func TestWarnStepAlignmentsOnANativeALB(t *testing.T) {
	for _, mode := range []timeseries.StepAlignment{0, timeseries.StepAlignmentOff} {
		c := replicaConfig("rr")
		c.Backends["replicas"].StepAlignment = mode
		warnStepAlignments(c)
		var want []string
		if mode != 0 {
			want = []string{`alb "replicas" sets step_alignment, which it applies to its http requests ` +
				`only; the members it sends native protocol sessions to use their own`}
		}
		require.Equal(t, want, stepAlignmentNotes(c), mode.String())
	}
	// a backend that isn't an alb is its own member
	c := replicaConfig("rr")
	c.Backends["replica-a"].StepAlignment = timeseries.StepAlignmentOff
	c.Backends["replica-a"].ListenerName = "mysql1"
	warnStepAlignments(c)
	require.Empty(t, stepAlignmentNotes(c))
}
