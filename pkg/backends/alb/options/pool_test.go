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
