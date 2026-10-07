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

const testResolver = "10.0.0.2:53"

func TestValidate(t *testing.T) {
	var nilOpts *Options
	require.NoError(t, nilOpts.Validate())

	tests := []struct {
		name string
		o    Options
		err  error
	}{
		{"default mode", Options{}, nil},
		{"mode a", Options{Mode: ModeA}, nil},
		{"mode srv", Options{Mode: "SRV"}, nil},
		{"srv with all settings", Options{
			Mode: ModeSRV, Resolver: testResolver,
			MinTTL: timeconv.Duration(time.Second), MaxTTL: timeconv.Duration(time.Minute),
			NegativeTTL: timeconv.Duration(time.Second), TLSServerName: ServerNameTarget,
		}, nil},
		{"bad mode", Options{Mode: "aaaa"}, ErrInvalidMode},
		{"srv-only setting with mode a", Options{Resolver: testResolver}, ErrSRVOnly},
		{"resolver without port", Options{Mode: ModeSRV, Resolver: "10.0.0.2"}, ErrInvalidResolver},
		{"resolver without host", Options{Mode: ModeSRV, Resolver: ":53"}, ErrInvalidResolver},
		{"negative ttl", Options{Mode: ModeSRV, NegativeTTL: -1}, ErrNegativeTTL},
		{"floor above ceiling", Options{
			Mode:   ModeSRV,
			MinTTL: timeconv.Duration(2 * time.Minute),
		}, ErrTTLFloorAboveCeiling},
		{"bad server name", Options{Mode: ModeSRV, TLSServerName: "url"}, ErrInvalidServerName},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.o.Validate()
			if test.err == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.err)
		})
	}
}

func TestInitialize(t *testing.T) {
	var nilOpts *Options
	nilOpts.Initialize()
	require.False(t, nilOpts.IsSRV())
	require.False(t, nilOpts.VerifiesTarget())
	require.Nil(t, nilOpts.Clone())

	o := &Options{Mode: " SRV ", TLSServerName: "Target", MinTTL: timeconv.Duration(time.Second)}
	o.Initialize()
	require.Equal(t, ModeSRV, o.Mode)
	require.Equal(t, ServerNameTarget, o.TLSServerName)
	require.True(t, o.VerifiesTarget())
	mn, mx, neg := o.TTLs()
	require.Equal(t, time.Second, mn)
	require.Equal(t, DefaultMaxTTL, mx)
	require.Equal(t, DefaultNegativeTTL, neg)
	require.Equal(t, timeconv.Duration(DefaultMaxTTL), o.MaxTTL)

	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	a := &Options{Mode: ModeA}
	a.Initialize()
	require.False(t, a.IsSRV())
	require.Zero(t, a.MaxTTL, "mode a takes no TTL defaults")
}
