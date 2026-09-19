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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestPoolMemberUnmarshal(t *testing.T) {
	var l PoolMemberList
	err := yaml.Unmarshal([]byte(`
- backend1
- name: backend2
  weight: 3
`), &l)
	require.NoError(t, err)
	require.Equal(t, PoolMemberList{
		{Name: "backend1"},
		{Name: "backend2", Weight: 3},
	}, l)
	require.Equal(t, []string{"backend1", "backend2"}, l.Names())
}

func TestPoolMemberMarshal(t *testing.T) {
	l := PoolMemberList{{Name: "b1"}, {Name: "b2", Weight: 3}}
	b, err := yaml.Marshal(l)
	require.NoError(t, err)
	out := string(b)
	require.Contains(t, out, "- b1\n", "unweighted members marshal as scalars")
	require.Contains(t, out, "name: b2")
	require.Contains(t, out, "weight: 3")
	// round trip
	var l2 PoolMemberList
	require.NoError(t, yaml.Unmarshal(b, &l2))
	require.Equal(t, l, l2)
}

func TestPoolMemberEffectiveWeight(t *testing.T) {
	require.Equal(t, 1, PoolMember{Name: "a"}.EffectiveWeight())
	require.Equal(t, 1, PoolMember{Name: "a", Weight: 1}.EffectiveWeight())
	require.Equal(t, 5, PoolMember{Name: "a", Weight: 5}.EffectiveWeight())
}

func TestPoolMemberListValidate(t *testing.T) {
	require.NoError(t, Members("a", "b").Validate("alb1"))
	err := PoolMemberList{{Name: "a", Weight: -1}}.Validate("alb1")
	require.ErrorIs(t, err, ErrInvalidPoolWeight)
	require.True(t, strings.Contains(err.Error(), "alb1"))
}

func TestValidatePoolRejectsNegativeWeight(t *testing.T) {
	o := &Options{Pool: PoolMemberList{{Name: "a", Weight: -2}}}
	err := o.ValidatePool("alb1", nil)
	require.ErrorIs(t, err, ErrInvalidPoolWeight)
}

func TestPoolMemberListDedupe(t *testing.T) {
	unique := PoolMemberList{{Name: "a"}, {Name: "b", Weight: 3}}
	got, repeats, err := unique.Dedupe("alb1")
	require.NoError(t, err)
	require.Empty(t, repeats)
	require.Equal(t, unique, got)

	// the first occurrence wins and keeps its position; each repeat is reported once with
	// the share the entries used to carry together
	got, repeats, err = PoolMemberList{
		{Name: "a"}, {Name: "b", Weight: 2}, {Name: "a"}, {Name: "c"}, {Name: "a"},
		{Name: "b", Weight: 2}, {Name: "d", Weight: 4}, {Name: "d"},
	}.Dedupe("alb1")
	require.NoError(t, err)
	require.Equal(t, PoolMemberList{
		{Name: "a"}, {Name: "b", Weight: 2}, {Name: "c"}, {Name: "d", Weight: 4},
	}, got)
	require.Equal(t, []PoolRepeat{
		{Name: "a", Count: 3, Weight: 3},
		{Name: "b", Count: 2, Weight: 4},
		{Name: "d", Count: 2, Weight: 5},
	}, repeats)

	_, _, err = PoolMemberList{{Name: "a", Weight: 2}, {Name: "b"}, {Name: "a", Weight: 3}}.Dedupe("alb1")
	require.ErrorIs(t, err, ErrConflictingPoolWeights)
	require.ErrorContains(t, err, `member "a" of alb "alb1"`)

	got, repeats, err = PoolMemberList(nil).Dedupe("alb1")
	require.NoError(t, err)
	require.Empty(t, got)
	require.Empty(t, repeats)
}

func TestInitializeDedupesPool(t *testing.T) {
	o := &Options{MechanismName: "rr", Pool: Members("a", "a", "b")}
	src := o.Pool
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, Members("a", "b"), o.Pool)
	require.Equal(t, Members("a", "a", "b"), src, "the configured list is not edited in place")
	warning := o.PoolRepeatWarning("alb1")
	require.Contains(t, warning, `alb "alb1"`)
	require.Contains(t, warning, "{name: a, weight: 2}")

	// a second pass finds nothing repeated and must not forget what the first one found
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, warning, o.PoolRepeatWarning("alb1"))
	c := o.Clone()
	require.Equal(t, o.PoolRepeats, c.PoolRepeats)
	c.PoolRepeats[0].Name = "changed"
	require.Equal(t, "a", o.PoolRepeats[0].Name)

	two := &Options{Pool: Members("a", "a", "b", "b", "b")}
	require.NoError(t, two.Initialize("alb2"))
	require.Contains(t, two.PoolRepeatWarning("alb2"), "{name: a, weight: 2}, {name: b, weight: 3}")

	require.Empty(t, (&Options{Pool: Members("a", "b")}).PoolRepeatWarning("alb3"))
	conflict := &Options{Pool: PoolMemberList{{Name: "a", Weight: 2}, {Name: "a", Weight: 5}}}
	require.ErrorIs(t, conflict.Initialize("alb4"), ErrConflictingPoolWeights)
}
