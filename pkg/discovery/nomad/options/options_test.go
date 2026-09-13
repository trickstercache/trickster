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

	consulopts "github.com/trickstercache/trickster/v2/pkg/discovery/consul/options"
	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// Nomad shares HashiCorp's blocking-query protocol with Consul, so the
// margin is deliberately the same one. If Consul's changes, this is what
// says whether Nomad meant to follow.
func TestPollTimeoutMatchesConsuls(t *testing.T) {
	for _, wait := range []time.Duration{time.Second, time.Minute, MaximumWait} {
		require.Equal(t, consulopts.PollTimeout(wait), PollTimeout(wait))
	}
}

// The bounds are Nomad's own, and are expected to match Consul's because
// both come from the same protocol.
func TestWaitBoundsMatchConsuls(t *testing.T) {
	require.Equal(t, consulopts.DefaultWait, DefaultWait)
	require.Equal(t, consulopts.MinimumWait, MinimumWait)
	require.Equal(t, consulopts.MaximumWait, MaximumWait)
}

func TestGetWaitDefaults(t *testing.T) {
	require.Equal(t, DefaultWait, (*Options)(nil).GetWait(),
		"a nil block behaves as an unset one")
	require.Equal(t, DefaultWait, New().GetWait())
	require.Equal(t, DefaultWait, (&Options{Wait: 0}).GetWait())
	require.Equal(t, DefaultWait, (&Options{Wait: -1}).GetWait(),
		"a negative wait is not a shorter wait")
	require.Equal(t, 45*time.Second,
		(&Options{Wait: timeconv.Duration(45 * time.Second)}).GetWait())
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
			err := (&Options{Wait: timeconv.Duration(tc.wait)}).Validate(0)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateRejectsATimeoutThatCannotOutlastTheWait(t *testing.T) {
	o := &Options{Wait: timeconv.Duration(time.Minute)}

	require.NoError(t, o.Validate(0), "an unset timeout is not yet a conflict")
	require.NoError(t, o.Validate(PollTimeout(time.Minute)),
		"the recommended timeout must itself be acceptable")

	err := o.Validate(time.Minute)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'http.timeout' must be greater than 'nomad.wait'",
		"the message must name nomad's own field, not consul's")
	require.Contains(t, err.Error(), PollTimeout(time.Minute).String())
}

func TestDefaultsAreMutuallyConsistent(t *testing.T) {
	o := New()
	require.NoError(t, o.Validate(PollTimeout(o.GetWait())))
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{
		Namespace: "prod", Region: "us-east",
		Wait: timeconv.Duration(time.Minute), AllowStale: true,
	}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Namespace = "staging"
	c.AllowStale = false
	require.Equal(t, "prod", o.Namespace)
	require.True(t, o.AllowStale)
}

func TestYAMLRoundTrip(t *testing.T) {
	const doc = `
namespace: prod
region: us-east
wait: 45s
allow_stale: true
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(doc), &o))
	require.Equal(t, "prod", o.Namespace)
	require.Equal(t, "us-east", o.Region)
	require.Equal(t, 45*time.Second, o.GetWait())
	require.True(t, o.AllowStale)
	require.NoError(t, o.Validate(PollTimeout(o.GetWait())))
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("registry", "'wait' must be at least 1s")
	require.EqualError(t, err,
		`invalid nomad options for discoverer "registry": 'wait' must be at least 1s`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
