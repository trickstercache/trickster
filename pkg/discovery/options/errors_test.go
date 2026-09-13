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
	"testing"

	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	gcpopts "github.com/trickstercache/trickster/v2/pkg/discovery/gcp/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"

	"github.com/stretchr/testify/require"
)

// The per-provider constructors live in each provider's own options package
// so that adding a provider does not mean editing this one. What that must
// not cost is the single error type: a caller recognizing a bad discovery
// config should not have to know which package built the error.
func TestProviderOptionsErrorsCarryTheSharedType(t *testing.T) {
	for name, err := range map[string]error{
		"aws":  awsopts.NewErrInvalidOptions("fleet", "detail"),
		"gcp":  gcpopts.NewErrInvalidOptions("fleet", "detail"),
		"http": NewErrInvalidHTTPOptions("fleet", "detail"),
		"name": NewErrInvalidDiscovererName("bad name"),
	} {
		t.Run(name, func(t *testing.T) {
			var target *derrors.InvalidDiscoveryOptionsError
			require.True(t, errors.As(err, &target))
		})
	}
}

// The messages an operator reads must not have shifted in the move.
func TestValidateReportsTheProviderBlockByName(t *testing.T) {
	o := &Options{
		Name:     "fleet",
		Provider: providers.GCP,
		GCP:      &gcpopts.Options{},
	}
	require.NoError(t, o.Initialize("fleet"))
	_, err := o.Validate()
	require.EqualError(t, err,
		`invalid gcp options for discoverer "fleet": `+
			`'service' is required and must be one of gce`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
