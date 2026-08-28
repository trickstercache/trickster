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
	"fmt"

	"go.yaml.in/yaml/v3"
)

// PoolMember is one ALB pool entry: a backend name with an optional integer
// load-balancing weight. In YAML a member may be a plain string (weight 1):
//
//	pool: [backend1, backend2]
//
// or a mapping with an explicit weight:
//
//	pool:
//	  - backend1
//	  - name: backend2
//	    weight: 3
//
// Weights apply to mechanisms that select a single member per request
// (round_robin); fan-out mechanisms dispatch to every member regardless of
// weight. A weight of 0 (or omitted) means 1. Weights replace the legacy
// workaround of repeating a member name to increase its share.
type PoolMember struct {
	Name   string `yaml:"name"`
	Weight int    `yaml:"weight,omitempty"`
}

// PoolMemberList is the ALB pool as configured
type PoolMemberList []PoolMember

// ErrInvalidPoolWeight is returned when a pool entry has a negative weight
var ErrInvalidPoolWeight = errors.New("pool member 'weight' cannot be negative")

// Members returns a PoolMemberList of the provided names, each with the
// default weight
func Members(names ...string) PoolMemberList {
	out := make(PoolMemberList, len(names))
	for i, n := range names {
		out[i] = PoolMember{Name: n}
	}
	return out
}

// Names returns the backend names of the list's members, in order
func (l PoolMemberList) Names() []string {
	out := make([]string, len(l))
	for i, m := range l {
		out[i] = m.Name
	}
	return out
}

// EffectiveWeight returns the member's weight for apportionment purposes;
// an unset (0) weight is 1
func (m PoolMember) EffectiveWeight() int {
	if m.Weight < 1 {
		return 1
	}
	return m.Weight
}

// UnmarshalYAML accepts either a plain backend-name scalar or a
// {name, weight} mapping
func (m *PoolMember) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		m.Weight = 0
		return value.Decode(&m.Name)
	}
	type loadPoolMember PoolMember
	var lm loadPoolMember
	if err := value.Decode(&lm); err != nil {
		return err
	}
	*m = PoolMember(lm)
	return nil
}

// MarshalYAML renders unweighted members as plain name scalars so sanitized
// config output matches the common input form
func (m PoolMember) MarshalYAML() (any, error) {
	if m.Weight == 0 {
		return m.Name, nil
	}
	type dumpPoolMember PoolMember
	return dumpPoolMember(m), nil
}

// Validate validates the list's weights
func (l PoolMemberList) Validate(albName string) error {
	for _, m := range l {
		if m.Weight < 0 {
			return fmt.Errorf("%w (member %q of alb %q)",
				ErrInvalidPoolWeight, m.Name, albName)
		}
	}
	return nil
}
