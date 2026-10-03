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

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

const flavorBackend = "cw"

// loadFlavor loads one backend from YAML and runs the config loader's Initialize then Validate.
func loadFlavor(t *testing.T, body string) (*Options, error) {
	t.Helper()
	o, err := fromYAML("backends:\n  "+flavorBackend+":\n"+body, flavorBackend)
	require.NoError(t, err)
	if err := o.Initialize(flavorBackend); err != nil {
		return o, err
	}
	_, err = o.Validate()
	return o, err
}

func TestCloudWatchFlavorDefaults(t *testing.T) {
	o, err := loadFlavor(t, `    provider: prometheus
    prometheus:
      flavor: cloudwatch
    sigv4:
      region: us-west-2
`)
	require.NoError(t, err)
	require.Equal(t, prop.CloudWatchOriginURL("us-west-2"), o.OriginURL)
	require.Equal(t, "monitoring.us-west-2.amazonaws.com", o.Host)
	require.Equal(t, prop.CloudWatchSigningService, o.SigV4.Service)
	require.Equal(t, timeconv.Duration(prop.CloudWatchVolatileWindow), o.VolatileWindow)
}

func TestCloudWatchFlavorCreatesSigV4(t *testing.T) {
	o, err := loadFlavor(t, `    provider: prometheus
    origin_url: https://monitoring.us-east-1.amazonaws.com
    prometheus:
      flavor: cloudwatch
`)
	require.NoError(t, err)
	require.NotNil(t, o.SigV4)
	require.Equal(t, prop.CloudWatchSigningService, o.SigV4.Service)
}

func TestCloudWatchFlavorKeepsExplicitValues(t *testing.T) {
	o, err := loadFlavor(t, `    provider: prometheus
    origin_url: https://proxy.example.com
    volatile_window: 0s
    prometheus:
      flavor: cloudwatch
    sigv4:
      region: us-east-1
      service: custom
`)
	require.NoError(t, err)
	require.Equal(t, "https://proxy.example.com", o.OriginURL)
	require.Equal(t, "custom", o.SigV4.Service)
	require.Zero(t, o.VolatileWindow, "an explicit volatile_window must not be replaced")

	o, err = loadFlavor(t, `    provider: prometheus
    origin_url: https://monitoring.us-east-1.amazonaws.com
    volatile_window_points: 3
    prometheus:
      flavor: cloudwatch
`)
	require.NoError(t, err)
	require.Zero(t, o.VolatileWindow)
	require.Equal(t, 3, o.VolatileWindowPoints)
}

func TestAMPFlavorCreatesSigV4(t *testing.T) {
	o, err := loadFlavor(t, `    provider: prometheus
    origin_url: https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-1
    prometheus:
      flavor: amp
`)
	require.NoError(t, err)
	require.NotNil(t, o.SigV4)
	require.Equal(t, "aps", o.SigV4.GetService())
	require.Equal(t, time.Duration(0), time.Duration(o.VolatileWindow))
}

func TestFlavorValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"unknown flavor", `    provider: prometheus
    origin_url: http://example.com
    prometheus:
      flavor: mimir
`, prop.ErrUnknownFlavor},
		{"wrong provider", `    provider: greptimedb
    origin_url: http://example.com
    prometheus:
      flavor: cloudwatch
`, ErrFlavorProvider},
		{"no origin and no region", `    provider: prometheus
    prometheus:
      flavor: cloudwatch
`, ErrFlavorMissingOrigin},
		{"cloudwatch region mismatch", `    provider: prometheus
    origin_url: https://monitoring.us-east-1.amazonaws.com
    prometheus:
      flavor: cloudwatch
    sigv4:
      region: us-west-2
`, ErrFlavorRegionMismatch},
		{"amp region mismatch", `    provider: prometheus
    origin_url: https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-1
    prometheus:
      flavor: amp
    sigv4:
      region: us-west-2
`, ErrFlavorRegionMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFlavor(t, tc.body)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestNoFlavorAddsNoSigV4(t *testing.T) {
	o, err := loadFlavor(t, `    provider: prometheus
    origin_url: http://example.com
`)
	require.NoError(t, err)
	require.Nil(t, o.SigV4)
	require.Zero(t, o.VolatileWindow)
}
