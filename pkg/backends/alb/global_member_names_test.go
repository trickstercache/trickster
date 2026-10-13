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

package alb

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
)

func TestValidateGlobalEvaluationMemberNames(t *testing.T) {
	const named = `alb "global" member "member": `
	for name, tc := range map[string]struct {
		member  backends.Backend
		wantErr error
		reason  string
	}{
		"influxdb": {
			newGlobalTestMember(t, providers.InfluxDB, nil), errors.ErrGlobalEvaluationMember,
			`provider "influxdb" is not a prometheus backend with a series_api`,
		},
		"prometheus without a series api": {newGlobalTestMember(t, providers.Prometheus, nil), prop.ErrSeriesEndpoint, ""},
		"thanos without an endpoint": {newGlobalTestMember(t, providers.Prometheus,
			&prop.Options{Flavor: prop.FlavorThanos}), prop.ErrSeriesEndpoint, ""},
		"mimir": {newGlobalTestMember(t, providers.Prometheus, &prop.Options{Flavor: prop.FlavorMimir}), nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateGlobalEvaluationMember(globalTestALB, tc.member)
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errors.ErrGlobalEvaluationMember)
			require.ErrorIs(t, err, tc.wantErr)
			require.ErrorContains(t, err, named+tc.reason)
		})
	}
}
