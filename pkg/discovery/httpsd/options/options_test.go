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

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestNewDefaultsToTricksterFormat(t *testing.T) {
	require.Equal(t, FormatTrickster, New().Format)
}

// GetFormat tolerates a nil receiver so callers need not distinguish an
// absent block from an unset field.
func TestGetFormatDefaults(t *testing.T) {
	require.Equal(t, FormatTrickster, (*Options)(nil).GetFormat())
	require.Equal(t, FormatTrickster, (&Options{}).GetFormat())
	require.Equal(t, FormatTrickster, (&Options{Format: FormatTrickster}).GetFormat())
	require.Equal(t, FormatPrometheus, (&Options{Format: FormatPrometheus}).GetFormat())
}

func TestInitializeFillsOnlyWhatIsUnset(t *testing.T) {
	o := &Options{}
	o.Initialize()
	require.Equal(t, FormatTrickster, o.Format)

	p := &Options{Format: FormatPrometheus}
	p.Initialize()
	require.Equal(t, FormatPrometheus, p.Format)

	require.NotPanics(t, func() { (*Options)(nil).Initialize() })
}

// The format is explicit rather than sniffed: the two documents are
// structurally distinguishable, but guessing means a typo in one can parse
// as a valid, wrong membership in the other -- a silently drained pool. So
// an unrecognized value must fail startup rather than fall back.
func TestValidateRejectsAnUnknownFormat(t *testing.T) {
	require.NoError(t, (&Options{Format: FormatTrickster}).Validate())
	require.NoError(t, (&Options{Format: FormatPrometheus}).Validate())
	require.NoError(t, (&Options{}).Validate(), "unset means the default")
	require.NoError(t, New().Validate())

	for _, bad := range []string{
		"promethius", "Prometheus", "TRICKSTER", "file_sd", "json",
	} {
		require.ErrorIs(t, (&Options{Format: bad}).Validate(), ErrInvalidFormat,
			"%q must not be accepted", bad)
	}
}

func TestFormatNamesAreDistinct(t *testing.T) {
	require.NotEqual(t, FormatTrickster, FormatPrometheus)
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{Format: FormatPrometheus}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Format = FormatTrickster
	require.Equal(t, FormatPrometheus, o.Format)
}

func TestYAMLRoundTrip(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("format: prometheus\n"), &o))
	require.Equal(t, FormatPrometheus, o.Format)
	require.NoError(t, o.Validate())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("sd", "'format' must be trickster or prometheus")
	require.EqualError(t, err,
		`invalid http_sd options for discoverer "sd": 'format' must be trickster or prometheus`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
