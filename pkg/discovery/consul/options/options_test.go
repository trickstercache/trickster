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
	// the same yaml package the config loader uses: timeconv.Duration's
	// UnmarshalYAML is written against its *yaml.Node, so testing with a
	// different yaml library would silently skip the custom unmarshaler
	"go.yaml.in/yaml/v3"
)

// The margin is not arbitrary: Consul adds up to wait/16 of its own jitter
// before returning, so a timeout of merely `wait` would abort perfectly
// healthy long polls.
func TestPollTimeoutCoversConsulsOwnJitter(t *testing.T) {
	wait := 5 * time.Minute
	got := PollTimeout(wait)
	require.Equal(t, wait+wait/16+WaitTimeoutFloor, got)
	require.Greater(t, got, wait, "the timeout must outlast the wait")
	require.GreaterOrEqual(t, got-wait, WaitTimeoutFloor,
		"the floor covers round-trip slack on top of the jitter")
}

func TestGetWaitDefaults(t *testing.T) {
	require.Equal(t, DefaultWait, (*Options)(nil).GetWait(),
		"a nil block behaves as an unset one")
	require.Equal(t, DefaultWait, New().GetWait())
	require.Equal(t, DefaultWait, (&Options{Wait: 0}).GetWait())
	require.Equal(t, DefaultWait, (&Options{Wait: -1}).GetWait(),
		"a negative wait is not a shorter wait")
	require.Equal(t, 30*time.Second,
		(&Options{Wait: timeconv.Duration(30 * time.Second)}).GetWait())
}

// Consul treats a warning check as still-serving for DNS purposes, so
// Trickster defaults to the same rather than draining those members.
func TestGetWarningIsReadyDefaultsTrue(t *testing.T) {
	require.True(t, (*Options)(nil).GetWarningIsReady())
	require.True(t, New().GetWarningIsReady())

	f, tr := false, true
	require.False(t, (&Options{WarningIsReady: &f}).GetWarningIsReady())
	require.True(t, (&Options{WarningIsReady: &tr}).GetWarningIsReady())
}

func TestValidateWaitBounds(t *testing.T) {
	tests := map[string]struct {
		wait time.Duration
		err  error
	}{
		"below minimum": {wait: time.Millisecond, err: ErrWaitTooLow},
		"at minimum":    {wait: MinimumWait},
		"at maximum":    {wait: MaximumWait},
		"above maximum": {wait: MaximumWait + time.Second, err: ErrWaitTooHigh},
		"well within":   {wait: time.Minute},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			o := &Options{Wait: timeconv.Duration(tc.wait)}
			err := o.Validate(0)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A timeout that does not outlast the wait aborts every long poll, turning
// an event-driven provider into a failing one. Catching it at startup beats
// a stream of timeouts in production.
func TestValidateRejectsATimeoutThatCannotOutlastTheWait(t *testing.T) {
	o := &Options{Wait: timeconv.Duration(time.Minute)}

	require.NoError(t, o.Validate(0), "an unset timeout is not yet a conflict")
	require.NoError(t, o.Validate(PollTimeout(time.Minute)),
		"the recommended timeout must itself be acceptable")

	for _, bad := range []time.Duration{time.Second, time.Minute} {
		err := o.Validate(bad)
		require.Error(t, err)
		require.Contains(t, err.Error(), "'http.timeout' must be greater than 'consul.wait'")
		require.Contains(t, err.Error(), PollTimeout(time.Minute).String(),
			"the message must name a timeout that would work")
	}
}

// The default wait is validated against the default-derived timeout, so the
// out-of-the-box configuration cannot be self-contradictory.
func TestDefaultsAreMutuallyConsistent(t *testing.T) {
	o := New()
	require.NoError(t, o.Validate(PollTimeout(o.GetWait())))
}

// WarningIsReady is a pointer so that "unset" is distinguishable from
// "false"; a shallow copy would alias it between two discoverers.
func TestCloneIsDeep(t *testing.T) {
	require.Nil(t, (*Options)(nil).Clone())

	f := false
	o := &Options{
		Datacenter: "dc1", Namespace: "ns", Partition: "part",
		Wait: timeconv.Duration(time.Minute), AllowStale: true,
		OnlyPassing: true, WarningIsReady: &f,
	}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)
	require.NotSame(t, o.WarningIsReady, c.WarningIsReady)

	*c.WarningIsReady = true
	require.False(t, *o.WarningIsReady, "the copy must not alias the original")

	c.Datacenter = "dc2"
	require.Equal(t, "dc1", o.Datacenter)
}

func TestCloneOfNilPointerField(t *testing.T) {
	c := (&Options{Datacenter: "dc1"}).Clone()
	require.Nil(t, c.WarningIsReady, "an unset tri-state stays unset")
}

func TestYAMLRoundTrip(t *testing.T) {
	const doc = `
datacenter: dc1
namespace: ns
partition: part
wait: 30s
allow_stale: true
only_passing: true
warning_is_ready: false
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(doc), &o))
	require.Equal(t, "dc1", o.Datacenter)
	require.Equal(t, "ns", o.Namespace)
	require.Equal(t, "part", o.Partition)
	require.Equal(t, 30*time.Second, o.GetWait())
	require.True(t, o.AllowStale)
	require.True(t, o.OnlyPassing)
	require.False(t, o.GetWarningIsReady())
	require.NoError(t, o.Validate(PollTimeout(o.GetWait())))
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("catalog", "'wait' must be at least 1s")
	require.EqualError(t, err,
		`invalid consul options for discoverer "catalog": 'wait' must be at least 1s`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target),
		"it must carry the shared type so callers can recognize it")
}
