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
	"time"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestNewAppliesTheDefaultInterval(t *testing.T) {
	require.Equal(t, timeconv.Duration(DefaultInterval), New().Interval)
	require.Empty(t, New().Resolver, "an empty resolver selects the system one")
}

func TestInitializeFillsOnlyWhatIsUnset(t *testing.T) {
	o := &Options{}
	o.Initialize()
	require.Equal(t, timeconv.Duration(DefaultInterval), o.Interval)

	custom := &Options{Interval: timeconv.Duration(5 * time.Second)}
	custom.Initialize()
	require.Equal(t, timeconv.Duration(5*time.Second), custom.Interval,
		"a configured interval must survive Initialize")

	require.NotPanics(t, func() { (*Options)(nil).Initialize() },
		"an absent block is not an error to default")
}

// The resolver is a host:port because a bare host gives no port to dial and
// would fail later, at query time, with a much worse message.
func TestValidateResolverMustBeHostPort(t *testing.T) {
	for _, good := range []string{
		"1.1.1.1:53", "127.0.0.1:5353", "dns.example.com:53", "[::1]:53",
	} {
		require.NoError(t, (&Options{Resolver: good}).Validate(), "%q", good)
	}
	for _, bad := range []string{"1.1.1.1", "dns.example.com", "::1"} {
		require.ErrorIs(t, (&Options{Resolver: bad}).Validate(),
			ErrInvalidResolver, "%q must be rejected", bad)
	}
	require.NoError(t, (&Options{}).Validate(),
		"an empty resolver is the system resolver, not an error")
}

func TestValidateIntervalFloor(t *testing.T) {
	require.ErrorIs(t,
		(&Options{Interval: timeconv.Duration(time.Millisecond)}).Validate(),
		ErrIntervalTooLow)
	require.NoError(t,
		(&Options{Interval: timeconv.Duration(MinimumInterval)}).Validate())
	require.NoError(t, (&Options{Interval: 0}).Validate(),
		"zero means unset, which Initialize fills with the default")
	require.NoError(t, New().Validate(), "the defaults must validate")
	require.NoError(t, (*Options)(nil).Validate(),
		"an absent block is valid; the provider supplies defaults")
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{Resolver: "1.1.1.1:53", Interval: timeconv.Duration(time.Minute)}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Resolver = "8.8.8.8:53"
	require.Equal(t, "1.1.1.1:53", o.Resolver)
}

func TestYAMLRoundTrip(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("resolver: 1.1.1.1:53\ninterval: 15s\n"), &o))
	require.Equal(t, "1.1.1.1:53", o.Resolver)
	require.Equal(t, timeconv.Duration(15*time.Second), o.Interval)
	require.NoError(t, o.Validate())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("corp-dns", "'resolver' must be a host:port")
	require.EqualError(t, err,
		`invalid dns options for discoverer "corp-dns": 'resolver' must be a host:port`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
