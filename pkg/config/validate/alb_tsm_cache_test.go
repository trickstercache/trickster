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

package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"

	"github.com/stretchr/testify/require"
)

const albCacheConfig = `
backends:
  prom:
    provider: prometheus
    origin_url: http://127.0.0.1:9090
  lb:
    provider: alb
    cache_name: missing
    alb:
      mechanism: MECHANISM
      pool: [{name: prom}]
      tsm: {evaluation: EVALUATION}
`

func TestALBCacheRequirementAgrees(t *testing.T) {
	const typo = "gl0bal"
	for name, test := range map[string]struct {
		mechanism, evaluation string
		wantCache             bool
		wantErr               error
	}{
		"residual global block on rr":    {names.MechanismRR, tsmoptions.EvaluationGlobal, false, nil},
		"global tsm without its cache":   {names.MechanismTSM, tsmoptions.EvaluationGlobal, true, nil},
		"global tsm under its long name": {"tsmerge", tsmoptions.EvaluationGlobal, true, nil},
		"global tsm under its registered long name": {
			names.MechanismTimeSeriesMerge, tsmoptions.EvaluationGlobal, true, nil,
		},
		"merging tsm":                   {names.MechanismTSM, tsmoptions.EvaluationMerge, false, nil},
		"typo in an active tsm block":   {names.MechanismTSM, typo, false, tsmoptions.ErrInvalidEvaluation},
		"typo in an inactive tsm block": {names.MechanismRR, typo, false, nil},
	} {
		t.Run(name, func(t *testing.T) {
			doc := strings.NewReplacer("MECHANISM", test.mechanism, "EVALUATION", test.evaluation).
				Replace(albCacheConfig)
			load := func() *config.Config { return loadCacheTestConfig(t, doc) }
			// the alb's cache_name names no cache, so startup fails only for an alb that keeps results
			c := load()
			err := Validate(c)
			switch {
			case test.wantErr != nil:
				require.ErrorIs(t, err, test.wantErr)
				return
			case test.wantCache:
				var cacheErr *bo.ErrInvalidCacheName
				require.ErrorAs(t, err, &cacheErr)
			default:
				require.NoError(t, err)
			}
			// route registration, with the backends unvalidated, needs the cache exactly when validation does
			if test.wantCache {
				c = load()
				require.NoError(t, Listeners(c))
			}
			require.NoError(t, c.Process())
			err = RoutesRulesAndPools(c, make(backends.Backends, len(c.Backends)))
			if test.wantCache {
				require.ErrorContains(t, err, "could not find cache named [missing]")
				return
			}
			require.NoError(t, err)
		})
	}
}

const residualALBOnOriginConfig = `
backends:
  web:
    provider: rp
    origin_url: http://127.0.0.1:9091
    cache_name: missing
    alb:
      mechanism: tsm
      tsm: {evaluation: global}
`

func TestResidualALBBlockOnOriginNeedsNoCache(t *testing.T) {
	// a non-alb backend never acts on a leftover alb block, so its global tsm settings ask for no cache
	c := loadCacheTestConfig(t, residualALBOnOriginConfig)
	require.NotNil(t, c.Backends["web"].ALBOptions)
	require.NoError(t, Validate(c))
	require.NoError(t, c.Process())
	require.NoError(t, RoutesRulesAndPools(c, make(backends.Backends, len(c.Backends))))
}

func loadCacheTestConfig(t *testing.T, doc string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	c, err := config.Load([]string{"-config", path})
	require.NoError(t, err)
	return c
}

const longNameConfig = `
backends:
  prom:
    provider: prometheus
    origin_url: http://127.0.0.1:9090
  lb:
    provider: alb
    alb:
      mechanism: MECHANISM
      pool: [{name: prom}]
`

func TestALBLongMechanismNames(t *testing.T) {
	// an alb named by a mechanism's long name loads, validates and starts as one named by its short name
	type outcome struct {
		mechanism, outputFormat string
		fgrCodes                bool
		validate, routes        string
	}
	errString := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	run := func(mechanism string) outcome {
		c := loadCacheTestConfig(t, strings.ReplaceAll(longNameConfig, "MECHANISM", mechanism))
		o := c.Backends["lb"].ALBOptions
		out := outcome{mechanism: o.MechanismName, outputFormat: o.OutputFormat, fgrCodes: o.FGRGoodCodes != nil}
		out.validate = errString(Validate(c))
		require.NoError(t, c.Process())
		out.routes = errString(RoutesRulesAndPools(c, make(backends.Backends, len(c.Backends))))
		return out
	}
	for long, short := range map[string]string{
		names.MechanismTimeSeriesMerge: names.MechanismTSM, names.MechanismFirstGoodResponse: names.MechanismFGR,
		names.MechanismFirstResponse: names.MechanismFR, names.MechanismNewestLastModified: names.MechanismNLM,
		names.MechanismUserRouter: names.MechanismUR,
	} {
		t.Run(long, func(t *testing.T) {
			want := run(short)
			require.Equal(t, short, want.mechanism)
			require.Equal(t, want, run(long))
		})
	}
}
