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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

const (
	tsmDefaultsBackend = "alb1"
	tsmGlobalALB       = `    provider: alb
    alb:
      mechanism: tsm
      tsm:
        evaluation: global
`
)

func TestTSMALBDefaults(t *testing.T) {
	tests := map[string]struct {
		body         string
		wantSize     int
		wantVolatile time.Duration
		wantPoints   int
		wantSet      bool
	}{
		"global tsm": {body: tsmGlobalALB, wantSize: DefaultTSMMaxObjectSizeBytes},
		"global tsm under its long name": {
			body: `    provider: alb
    alb:
      mechanism: tsmerge
      tsm:
        evaluation: global
`,
			wantSize: DefaultTSMMaxObjectSizeBytes,
		},
		"merging tsm": {
			body: `    provider: alb
    alb:
      mechanism: tsm
`,
			wantSize: DefaultTSMMaxObjectSizeBytes,
		},
		"round robin": {
			body: `    provider: alb
    alb:
      mechanism: rr
`,
			wantSize: DefaultMaxObjectSizeBytes,
		},
		"user router": {
			body: `    provider: alb
    alb:
      mechanism: ur
`,
			wantSize: DefaultMaxObjectSizeBytes,
		},
		"a member": {
			body: `    provider: prometheus
    origin_url: http://mimir:8080/prometheus
    prometheus:
      flavor: mimir
`,
			wantSize: DefaultMaxObjectSizeBytes,
		},
		"explicit size": {body: tsmGlobalALB + "    max_object_size_bytes: 1024\n", wantSize: 1024},
		"volatile window": {
			body:     tsmGlobalALB + "    volatile_window: 1m\n",
			wantSize: DefaultTSMMaxObjectSizeBytes, wantVolatile: time.Minute, wantSet: true,
		},
		"no volatile window": {
			body:     tsmGlobalALB + "    volatile_window: 0s\n",
			wantSize: DefaultTSMMaxObjectSizeBytes, wantSet: true,
		},
		"legacy volatile window": {
			body:     tsmGlobalALB + "    backfill_tolerance: 2m\n",
			wantSize: DefaultTSMMaxObjectSizeBytes, wantVolatile: 2 * time.Minute, wantSet: true,
		},
		"volatile points only": {
			body:     tsmGlobalALB + "    volatile_window_points: 3\n",
			wantSize: DefaultTSMMaxObjectSizeBytes, wantPoints: 3,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			o, err := fromYAML("backends:\n  "+tsmDefaultsBackend+":\n"+tc.body, tsmDefaultsBackend)
			require.NoError(t, err)
			require.NoError(t, o.Initialize(tsmDefaultsBackend))
			require.Equal(t, tc.wantSize, o.MaxObjectSizeBytes)
			require.Equal(t, timeconv.Duration(tc.wantVolatile), o.VolatileWindow)
			require.Equal(t, tc.wantPoints, o.VolatileWindowPoints)
			require.Equal(t, tc.wantSet, o.HasVolatileWindow())
			// a reload initializes a clone, which keeps what the file set
			c := o.Clone()
			require.NoError(t, c.Initialize(tsmDefaultsBackend))
			require.Equal(t, o.VolatileWindow, c.VolatileWindow)
			require.Equal(t, tc.wantSet, c.HasVolatileWindow())
		})
	}
}

func TestHasVolatileWindowInCode(t *testing.T) {
	var none *Options
	require.False(t, none.HasVolatileWindow())
	o := New()
	require.False(t, o.HasVolatileWindow())
	o.VolatileWindow = timeconv.Duration(time.Minute)
	require.True(t, o.HasVolatileWindow())
}
