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

package tsm

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/errors"

	"github.com/stretchr/testify/require"
)

func TestRegistryEntryConfig(t *testing.T) {
	t.Parallel()
	limit, multiplier, tolerance := 3, 2, 5
	o := &options.Options{
		OutputFormat:          providers.Prometheus,
		MaxCaptureBytes:       1024,
		MaxFanoutCaptureBytes: 4096,
		TSMOptions: options.TimeSeriesMergeOptions{
			ConcurrencyOptions: options.ConcurrencyOptions{
				QueryConcurrencyLimit:      &limit,
				QueryConcurrencyMultiplier: &multiplier,
			},
			DedupToleranceMs: &tolerance,
		},
	}
	m, err := RegistryEntry().New(o, rt.Lookup{providers.Prometheus: prometheus.NewClient})
	require.NoError(t, err)
	h := m.(*handler)
	require.Equal(t, o.TSMOptions, h.tsmOptions)
	require.Equal(t, 6, h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit())
	require.Equal(t, int64(5_000_000), h.dedupToleranceNanos())
	require.Equal(t, o.MaxCaptureBytes, h.maxCaptureBytes)
	require.Equal(t, o.MaxFanoutCaptureBytes, h.maxFanoutCaptureBytes)
	require.Equal(t, o.OutputFormat, h.outputFormat)
	require.NotNil(t, h.queryParser)
	require.NotEmpty(t, h.mergePaths)
}

func TestRegistryEntryNilOptions(t *testing.T) {
	t.Parallel()
	m, err := RegistryEntry().New(nil, nil)
	require.ErrorIs(t, err, errors.ErrInvalidOptions)
	require.Nil(t, m)
}
