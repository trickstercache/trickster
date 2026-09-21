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
// weight. A weight of 0 (or omitted) means 1. A weight is the only way to
// increase a member's share: a name repeated in the list is de-duplicated.
//
// A backup member stands by: it is used only while no other member is available.
type PoolMember struct {
	Name   string `yaml:"name"`
	Weight int    `yaml:"weight,omitempty"`
	Backup bool   `yaml:"backup,omitempty"`
}

// BackupTier is the failover tier of a backup member; every other member is in tier 0
const BackupTier = 1

// PoolMemberList is the ALB pool as configured
type PoolMemberList []PoolMember

// ErrInvalidPoolWeight is returned when a pool entry has a negative weight
var ErrInvalidPoolWeight = errors.New("pool member 'weight' cannot be negative")

// ErrNoPrimaryPoolMember is returned when every member of a pool is a backup
var ErrNoPrimaryPoolMember = errors.New("pool needs at least one member that is not a 'backup'")

// ErrConflictingPoolWeights is returned when a pool lists one member under different weights
var ErrConflictingPoolWeights = errors.New("pool member is repeated with different 'weight' values")

// PoolRepeat describes a member name that a pool listed more than once
type PoolRepeat struct {
	Name string
	// Count is how many times the name was listed
	Count int
	// Weight is the combined effective weight the repeated entries carried
	Weight int
}

// Dedupe returns the list with repeated member names removed, the first occurrence winning,
// and what was repeated. Repeats that set different explicit weights are an error.
func (l PoolMemberList) Dedupe(albName string) (PoolMemberList, []PoolRepeat, error) {
	seen := make(map[string]int, len(l))
	var repeats []PoolRepeat
	var repeatIdx map[string]int
	out := l
	for i, m := range l {
		j, dup := seen[m.Name]
		if !dup {
			seen[m.Name] = i
			if repeats != nil {
				out = append(out, m)
			}
			continue
		}
		first := l[j]
		if first.Weight > 0 && m.Weight > 0 && first.Weight != m.Weight {
			return nil, nil, fmt.Errorf("%w (member %q of alb %q: %d and %d)",
				ErrConflictingPoolWeights, m.Name, albName, first.Weight, m.Weight)
		}
		if repeats == nil {
			// the first repeat: everything before it is kept as is
			out = append(make(PoolMemberList, 0, len(l)-1), l[:i]...)
			repeatIdx = make(map[string]int)
		}
		k, ok := repeatIdx[m.Name]
		if !ok {
			k = len(repeats)
			repeatIdx[m.Name] = k
			repeats = append(repeats, PoolRepeat{Name: m.Name, Count: 1, Weight: first.EffectiveWeight()})
		}
		repeats[k].Count++
		repeats[k].Weight += m.EffectiveWeight()
	}
	return out, repeats, nil
}

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

// Tier returns the member's failover tier
func (m PoolMember) Tier() int {
	if m.Backup {
		return BackupTier
	}
	return 0
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
	if m.Weight == 0 && !m.Backup {
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

// AllBackups reports whether the list has members and every one of them is a backup
func (l PoolMemberList) AllBackups() bool {
	for _, m := range l {
		if !m.Backup {
			return false
		}
	}
	return len(l) > 0
}
