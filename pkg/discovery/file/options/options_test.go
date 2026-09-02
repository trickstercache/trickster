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

func TestNewAppliesTheDefaultPollInterval(t *testing.T) {
	require.Equal(t, timeconv.Duration(DefaultPollInterval), New().PollInterval)
}

func TestInitializeFillsOnlyWhatIsUnset(t *testing.T) {
	o := &Options{}
	o.Initialize()
	require.Equal(t, timeconv.Duration(DefaultPollInterval), o.PollInterval)

	// on NFS or a FUSE/CSI mount the poll is the effective update
	// mechanism, so a deliberately low cadence must survive Initialize
	custom := &Options{PollInterval: timeconv.Duration(2 * time.Second)}
	custom.Initialize()
	require.Equal(t, timeconv.Duration(2*time.Second), custom.PollInterval)

	require.NotPanics(t, func() { (*Options)(nil).Initialize() })
}

func TestValidatePollIntervalFloor(t *testing.T) {
	require.ErrorIs(t,
		(&Options{PollInterval: timeconv.Duration(time.Millisecond)}).Validate(),
		ErrPollIntervalTooLow)
	require.NoError(t,
		(&Options{PollInterval: timeconv.Duration(MinimumPollInterval)}).Validate())
	require.NoError(t, (&Options{PollInterval: 0}).Validate(),
		"zero means unset, which Initialize fills with the default")
	require.NoError(t, New().Validate(), "the defaults must validate")
	require.NoError(t, (*Options)(nil).Validate(),
		"an absent block is valid; the provider supplies defaults")
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{PollInterval: timeconv.Duration(time.Minute)}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.PollInterval = timeconv.Duration(time.Hour)
	require.Equal(t, timeconv.Duration(time.Minute), o.PollInterval)
}

func TestYAMLRoundTrip(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("poll_interval: 5s\n"), &o))
	require.Equal(t, timeconv.Duration(5*time.Second), o.PollInterval)
	require.NoError(t, o.Validate())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("members", "'poll_interval' must be at least 1s")
	require.EqualError(t, err,
		`invalid file options for discoverer "members": 'poll_interval' must be at least 1s`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
