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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

func TestRenamedKeys(t *testing.T) {
	type field struct {
		newKey, legacyKey string
		newVal, legacyVal string
		get               func(*Options) any
		wantNew, wantOld  any
		def               any
	}
	fields := []field{
		{
			"partial_bucket_ttl", "fastforward_ttl", "20s", "30s",
			func(o *Options) any { return o.PartialBucketTTL },
			timeconv.Duration(20 * time.Second), timeconv.Duration(30 * time.Second),
			timeconv.Duration(DefaultPartialBucketTTL),
		},
	}
	for _, f := range fields {
		cases := []struct {
			name, keys string
			want       any
		}{
			{"new key only", "    " + f.newKey + ": " + f.newVal + "\n", f.wantNew},
			{"legacy key only", "    " + f.legacyKey + ": " + f.legacyVal + "\n", f.wantOld},
			{"both keys, new wins", "    " + f.legacyKey + ": " + f.legacyVal + "\n    " +
				f.newKey + ": " + f.newVal + "\n", f.wantNew},
			{"neither key", "", f.def},
		}
		for _, c := range cases {
			t.Run(f.newKey+"/"+c.name, func(t *testing.T) {
				o, err := fromYAML("backends:\n  test:\n    provider: prometheus\n"+c.keys, "test")
				require.NoError(t, err)
				require.Equal(t, c.want, f.get(o))
			})
		}
	}

	t.Run("dumps emit only the new keys", func(t *testing.T) {
		o, err := fromYAML(`
backends:
  test:
    provider: clickhouse
    fastforward_ttl: 30s
`, "test")
		require.NoError(t, err)
		out := o.ToYAML()
		require.NotContains(t, out, "fastforward_ttl")
		require.Contains(t, out, "partial_bucket_ttl: 30s")
	})
}

func TestStepAlignmentYAML(t *testing.T) {
	o, err := fromYAML("backends:\n  test:\n    provider: clickhouse\n    step_alignment: Drop\n", "test")
	require.NoError(t, err)
	require.Equal(t, timeseries.StepAlignmentDrop, o.StepAlignment)
	require.Contains(t, o.ToYAML(), "step_alignment: drop")
	require.Equal(t, timeseries.StepAlignmentDrop, o.Clone().StepAlignment)

	o, err = fromYAML("backends:\n  test:\n    provider: clickhouse\n", "test")
	require.NoError(t, err)
	require.Zero(t, o.StepAlignment)
	require.NotContains(t, o.ToYAML(), "step_alignment")

	_, err = fromYAML("backends:\n  test:\n    provider: clickhouse\n    step_alignment: exact\n", "test")
	require.ErrorIs(t, err, timeseries.ErrInvalidStepAlignment)
}

func TestValidateStepAlignmentWithFastForwardDisable(t *testing.T) {
	const base = "backends:\n  test:\n    provider: %s\n    origin_url: http://127.0.0.1\n"
	tests := []struct {
		name, provider, keys string
		wantErr              bool
	}{
		{"prometheus neither key", providers.Prometheus, "", false},
		{"prometheus step_alignment only", providers.Prometheus, "    step_alignment: truncate\n", false},
		{"prometheus fast_forward_disable only", providers.Prometheus, "    fast_forward_disable: true\n", false},
		{
			"prometheus both keys", providers.Prometheus,
			"    step_alignment: truncate\n    fast_forward_disable: true\n", true,
		},
		{
			"prometheus both keys, ffd false", providers.Prometheus,
			"    step_alignment: partial_end\n    fast_forward_disable: false\n", true,
		},
		{
			"prometheus both keys, empty mode", providers.Prometheus,
			"    step_alignment: ''\n    fast_forward_disable: false\n", true,
		},
		{
			"other providers ignore the pair", providers.ClickHouse,
			"    step_alignment: drop\n    fast_forward_disable: true\n", false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o, err := fromYAML(strings.Replace(base, "%s", test.provider, 1)+test.keys, "test")
			require.NoError(t, err)
			o.Name = "test"
			_, err = o.Validate()
			if test.wantErr {
				require.True(t, errors.Is(err, ErrStepAlignmentWithFastForwardDisable), "got %v", err)
				return
			}
			require.NoError(t, err)
		})
	}

	t.Run("options built in code", func(t *testing.T) {
		o := New()
		o.Name, o.Provider, o.OriginURL = "test", providers.Prometheus, "http://127.0.0.1"
		o.StepAlignment, o.FastForwardDisable = timeseries.StepAlignmentTruncate, true
		_, err := o.Validate()
		require.ErrorIs(t, err, ErrStepAlignmentWithFastForwardDisable)
	})
}

func TestVolatileWindowFallbackKeys(t *testing.T) {
	const base = "backends:\n  test:\n    provider: clickhouse\n    origin_url: http://127.0.0.1\n"
	for _, pair := range []struct {
		key, fallback, val, fallbackVal string
		get                             func(*Options) any
		want, wantFallback, def         any
		conflict                        error
	}{
		{
			"volatile_window", "backfill_tolerance", "40s", "50s",
			func(o *Options) any { return o.VolatileWindow },
			timeconv.Duration(40 * time.Second), timeconv.Duration(50 * time.Second),
			timeconv.Duration(DefaultVolatileWindow), ErrVolatileWindowWithBackfillTolerance,
		},
		{
			"volatile_window_points", "backfill_tolerance_points", "3", "4",
			func(o *Options) any { return o.VolatileWindowPoints },
			3, 4, DefaultVolatileWindowPoints, ErrVolatileWindowPointsWithBackfillTolerancePoints,
		},
	} {
		for _, test := range []struct {
			name, keys string
			want       any
			err        error
		}{
			{"its own key", "    " + pair.key + ": " + pair.val + "\n", pair.want, nil},
			{"the fallback key", "    " + pair.fallback + ": " + pair.fallbackVal + "\n", pair.wantFallback, nil},
			{"neither key", "", pair.def, nil},
			{
				"both keys", "    " + pair.fallback + ": " + pair.fallbackVal + "\n    " + pair.key + ": " + pair.val + "\n",
				pair.want, pair.conflict,
			},
		} {
			t.Run(pair.key+"/"+test.name, func(t *testing.T) {
				o, err := fromYAML(base+test.keys, "test")
				require.NoError(t, err)
				require.Equal(t, test.want, pair.get(o))
				o.Name = "test"
				_, err = o.Validate()
				if test.err != nil {
					require.ErrorIs(t, err, test.err)
					return
				}
				require.NoError(t, err)
				// a dump names only the volatile window keys
				require.NotContains(t, o.ToYAML(), pair.fallback)
			})
		}
	}
}

func TestStepAlignmentErrors(t *testing.T) {
	err := NewErrUnsupportedStepAlignment(timeseries.StepAlignmentPartial,
		timeseries.StepAlignmentTruncate|timeseries.StepAlignmentOff, providers.Graphite, "g1")
	require.ErrorIs(t, err, ErrUnsupportedStepAlignment)
	require.Equal(t, `unsupported step_alignment "partial" for backend "g1": provider "graphite" supports off, truncate`,
		err.Error())
}
