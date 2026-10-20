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

	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

const globalDiscoveryConfig = `
discovery:
  in-cluster:
    provider: kubernetes
backends:
  tmpl:
    provider: PROVIDER
    is_template: true
    prometheus: {PROMETHEUS}
  lb:
    provider: alb
    alb:
      mechanism: tsmerge
      tsm: {evaluation: EVALUATION}
      discovery:
        discoverer_name: in-cluster
        template_backend: tmpl
        query: {namespace: monitoring, service: prom, scheme: "SCHEME"}
`

const (
	sharedEndpointWarning = `gives every discovered member the series_endpoint of template "tmpl"`
	noFlavor              = `flavor: ""`
	mimirFlavor           = "flavor: mimir"
	thanosStore           = "series_endpoint: grpc://thanos:10901"
	thanosFlavor          = "flavor: thanos, " + thanosStore
)

func loadGlobalDiscoveryConfig(t *testing.T, provider, prometheus, evaluation, scheme string) (*config.Config, error) {
	t.Helper()
	doc := strings.NewReplacer("PROVIDER", provider, "PROMETHEUS", prometheus,
		"EVALUATION", evaluation, "SCHEME", scheme).Replace(globalDiscoveryConfig)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	c, err := config.Load([]string{"-config", path})
	require.NoError(t, err)
	return c, Validate(c)
}

func TestValidateGlobalEvaluationDiscoveryTemplate(t *testing.T) {
	const (
		merge  = tsmoptions.EvaluationMerge
		global = tsmoptions.EvaluationGlobal
	)
	for name, test := range map[string]struct {
		provider, flavor, evaluation string
		wantGlobal, wantTSM          bool
	}{
		"influxdb on merge":               {providers.InfluxDB, `""`, merge, false, true},
		"influxdb on global":              {providers.InfluxDB, `""`, global, true, false},
		"greptimedb on merge":             {providers.GreptimeDB, `""`, merge, false, false},
		"greptimedb on global":            {providers.GreptimeDB, `""`, global, true, false},
		"prometheus without a series api": {providers.Prometheus, `""`, global, true, false},
		"thanos without an endpoint":      {providers.Prometheus, "thanos", global, true, false},
		"mimir":                           {providers.Prometheus, "mimir", global, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadGlobalDiscoveryConfig(t, test.provider, "flavor: "+test.flavor, test.evaluation, "")
			var tsmErr *bo.ErrInvalidTemplateTSMProvider
			switch {
			case test.wantGlobal:
				require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
				require.ErrorContains(t, err, `alb "lb" template "tmpl": `)
			case test.wantTSM:
				require.ErrorAs(t, err, &tsmErr)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateGlobalEvaluationDiscoveryScheme(t *testing.T) {
	for name, test := range map[string]struct {
		prometheus, evaluation, scheme string
		wantErr                        bool
	}{
		"mimir":                {mimirFlavor, tsmoptions.EvaluationGlobal, "", false},
		"mimir over https":     {mimirFlavor, tsmoptions.EvaluationGlobal, do.SchemeHTTPS, false},
		"mimir over tcp":       {mimirFlavor, tsmoptions.EvaluationGlobal, do.SchemeTCP, true},
		"mimir over udp":       {mimirFlavor, tsmoptions.EvaluationGlobal, do.SchemeUDP, true},
		"mimir over tcp merge": {mimirFlavor, tsmoptions.EvaluationMerge, do.SchemeTCP, false},
		"thanos over tcp":      {thanosFlavor, tsmoptions.EvaluationGlobal, do.SchemeTCP, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadGlobalDiscoveryConfig(t, providers.Prometheus, test.prometheus, test.evaluation, test.scheme)
			if !test.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
			require.ErrorIs(t, err, prop.ErrSeriesEndpoint)
			require.ErrorContains(t, err, `alb "lb" template "tmpl": `)
		})
	}
}

func TestValidateGlobalEvaluationDiscoveryTypos(t *testing.T) {
	for name, test := range map[string]struct {
		prometheus string
		wantErr    error
	}{
		"misspelled series api": {"series_api: remoteread", prop.ErrUnknownSeriesAPI},
		"miscased storeapi":     {"series_api: storeAPI, " + thanosStore, prop.ErrUnknownSeriesAPI},
		"miscased flavor":       {"flavor: Mimir", prop.ErrUnknownFlavor},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadGlobalDiscoveryConfig(t, providers.Prometheus, test.prometheus,
				tsmoptions.EvaluationGlobal, "")
			require.ErrorIs(t, err, test.wantErr)
			require.ErrorContains(t, err, `alb "lb" template "tmpl": `)
		})
	}
}

func TestValidateGlobalEvaluationSharedSeriesEndpoint(t *testing.T) {
	const readEndpoint = "series_api: remote_read, series_endpoint: http://mimir:8080/prometheus/api/v1/read"
	for name, test := range map[string]struct {
		prometheus, evaluation string
		wantWarning            bool
	}{
		"thanos":                  {thanosFlavor, tsmoptions.EvaluationGlobal, true},
		"remote read endpoint":    {readEndpoint, tsmoptions.EvaluationGlobal, true},
		"mimir":                   {mimirFlavor, tsmoptions.EvaluationGlobal, false},
		"thanos on merge":         {thanosFlavor, tsmoptions.EvaluationMerge, false},
		"remote read on merge":    {readEndpoint, tsmoptions.EvaluationMerge, false},
		"no series api, on merge": {noFlavor, tsmoptions.EvaluationMerge, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := loadGlobalDiscoveryConfig(t, providers.Prometheus, test.prometheus, test.evaluation, "")
			require.NoError(t, err)
			warnings := strings.Join(c.LoaderWarnings, "\n")
			if !test.wantWarning {
				require.NotContains(t, warnings, sharedEndpointWarning)
				return
			}
			require.Equal(t, 1, strings.Count(warnings, sharedEndpointWarning), warnings)
			require.Contains(t, warnings, `alb "lb"`)
		})
	}
}
