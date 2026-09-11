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

package options

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestTSMYAMLLayout(t *testing.T) {
	t.Parallel()
	const input = `mechanism: tsm
output_format: prometheus
max_capture_bytes: 1024
max_fanout_capture_bytes: 4096
tsm:
  query_concurrency_limit: 3
  query_concurrency_multiplier: 2
  dedup_tolerance_ms: 5
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(input), &o))
	require.Equal(t, providers.Prometheus, o.OutputFormat)
	require.Equal(t, 1024, o.MaxCaptureBytes)
	require.Equal(t, 4096, o.MaxFanoutCaptureBytes)
	require.Equal(t, 3, *o.TSMOptions.ConcurrencyOptions.QueryConcurrencyLimit)
	require.Equal(t, 2, *o.TSMOptions.ConcurrencyOptions.QueryConcurrencyMultiplier)
	require.Equal(t, 5, *o.TSMOptions.DedupToleranceMs)
	require.NoError(t, o.Initialize(""))
	ok, err := o.Validate()
	require.NoError(t, err)
	require.True(t, ok)

	data, err := yaml.Marshal(&o)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, yaml.Unmarshal(data, &fields))
	require.Equal(t, providers.Prometheus, fields["output_format"])
	tsmFields := fields["tsm"].(map[string]any)
	require.NotContains(t, tsmFields, "output_format")
	require.Equal(t, 3, tsmFields["query_concurrency_limit"])
	require.Equal(t, 2, tsmFields["query_concurrency_multiplier"])
	require.Equal(t, 5, tsmFields["dedup_tolerance_ms"])

	var roundTrip Options
	require.NoError(t, yaml.Unmarshal(data, &roundTrip))
	require.Equal(t, o, roundTrip)
}

func TestOutputFormatYAMLValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		input      string
		wantFormat string
		wantErr    error
	}{
		{"explicit", "mechanism: tsm\noutput_format: prometheus\n", providers.Prometheus, nil},
		{"default", "mechanism: tsm\n", providers.Prometheus, nil},
		{"legacy mechanism", "mechanism: tsmerge\noutput_format: prometheus\n", providers.Prometheus, nil},
		{"invalid provider", "mechanism: tsm\noutput_format: not-a-provider\n", "not-a-provider", ErrInvalidOutputFormat},
		{"non tsm", "mechanism: rr\noutput_format: prometheus\n", providers.Prometheus, ErrOutputFormatOnlyForTSM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var o Options
			require.NoError(t, yaml.Unmarshal([]byte(tc.input), &o))
			require.NoError(t, o.Initialize(""))
			require.Equal(t, tc.wantFormat, o.OutputFormat)
			ok, err := o.Validate()
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, tc.wantErr == nil, ok)
		})
	}
}
