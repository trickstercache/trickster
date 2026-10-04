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
package options

import (
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	sticky "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"github.com/stretchr/testify/require"
)

func TestStickyBlock(t *testing.T) {
	o := load(t, "mechanism: p2c\nsticky:\n  mode: table\n  table: {key: 'header:X-Tenant'}\n")
	require.NoError(t, o.Initialize("alb1"))
	ok, err := o.Validate()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sticky.OnUnavailableRepick, o.Sticky.OnUnavailable, "the block is initialized with the ALB")
	require.Equal(t, "X-Tenant", o.Sticky.Table.KeySource.Name)

	c := o.Clone()
	require.Equal(t, o.Sticky, c.Sticky)
	require.NotSame(t, o.Sticky, c.Sticky)

	require.Nil(t, New().Clone().Sticky)
}

func TestStickyNeedsAMechanismThatPicksOne(t *testing.T) {
	for _, mech := range []string{
		names.MechanismRR, names.MechanismPowerOfTwoChoices, names.MechanismLC,
		names.MechanismLeastTime, names.MechanismHRW,
	} {
		o := load(t, "mechanism: "+mech+"\nsticky: {}\n")
		require.NoError(t, o.Initialize("alb1"))
		_, err := o.Validate()
		require.NoError(t, err, mech)
	}
	for _, mech := range []string{
		names.MechanismFR, names.MechanismFGR, names.MechanismNLM, names.MechanismTSM,
		names.MechanismRace, names.MechanismMirror, names.MechanismUDPMirror, "",
	} {
		o := load(t, "mechanism: "+mech+"\nsticky: {}\n")
		require.NoError(t, o.Initialize("alb1"))
		_, err := o.Validate()
		require.ErrorIs(t, err, ErrStickyMechanism, mech)
	}
	o := load(t, "mechanism: ur\nuser_router: {}\nsticky: {}\n")
	require.NoError(t, o.Initialize("alb1"))
	_, err := o.Validate()
	require.ErrorIs(t, err, ErrStickyMechanism)
}

func TestStickyErrorsReachTheALB(t *testing.T) {
	o := load(t, "mechanism: rr\nsticky: {secret: short}\n")
	require.ErrorIs(t, o.Initialize("alb1"), secret.ErrKeyTooShort)

	o = load(t, "mechanism: rr\nsticky: {mode: always}\n")
	require.NoError(t, o.Initialize("alb1"))
	_, err := o.Validate()
	require.ErrorIs(t, err, sticky.ErrInvalidMode)
	require.True(t, strings.Contains(err.Error(), "'sticky.mode'"))
}
