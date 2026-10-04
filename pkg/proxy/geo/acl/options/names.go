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
	"errors"
	"fmt"
	"strings"
)

// Action is what a geo ACL does with a client it denies
type Action uint8

const (
	// ActionReject refuses the client
	ActionReject Action = iota + 1
	// ActionCount counts the denial and allows the client
	ActionCount
)

// action names, as configured
const (
	ActionNameReject = "reject"
	ActionNameCount  = "count"
)

var actionNames = [...]string{ActionNameReject, ActionNameCount}

// ErrInvalidAction is returned when a name matches no action
var ErrInvalidAction = errors.New("invalid geo ACL action")

// ParseAction returns the action named by name, ignoring case and surrounding space; an empty name returns
// the zero value, meaning none was chosen
func ParseAction(name string) (Action, error) {
	return parseName[Action](name, actionNames[:], ErrInvalidAction)
}

// String returns the action's name, or an empty string for the zero value
func (a Action) String() string {
	return nameOf(a, actionNames[:])
}

// MarshalText returns the name of the action
func (a Action) MarshalText() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalText sets a to the action named by text
func (a *Action) UnmarshalText(text []byte) error {
	v, err := ParseAction(string(text))
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// Verdict is the judgment a geo ACL gives a client it cannot place
type Verdict uint8

const (
	// VerdictAllow allows the client
	VerdictAllow Verdict = iota + 1
	// VerdictDeny denies the client
	VerdictDeny
)

// verdict names, as configured
const (
	VerdictNameAllow = "allow"
	VerdictNameDeny  = "deny"
)

var verdictNames = [...]string{VerdictNameAllow, VerdictNameDeny}

// ErrInvalidVerdict is returned when a name matches no verdict
var ErrInvalidVerdict = errors.New("invalid geo ACL unknown verdict")

// ParseVerdict returns the verdict named by name, ignoring case and surrounding space; an empty name returns
// the zero value, meaning none was chosen
func ParseVerdict(name string) (Verdict, error) {
	return parseName[Verdict](name, verdictNames[:], ErrInvalidVerdict)
}

// String returns the verdict's name, or an empty string for the zero value
func (v Verdict) String() string {
	return nameOf(v, verdictNames[:])
}

// MarshalText returns the name of the verdict
func (v Verdict) MarshalText() ([]byte, error) {
	return []byte(v.String()), nil
}

// UnmarshalText sets v to the verdict named by text
func (v *Verdict) UnmarshalText(text []byte) error {
	p, err := ParseVerdict(string(text))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

func parseName[T ~uint8](name string, names []string, sentinel error) (T, error) {
	// a name's value is its one-based position in names, and the zero value is an empty name
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	var v T
	for _, n := range names {
		v++
		if strings.EqualFold(name, n) {
			return v, nil
		}
	}
	return 0, fmt.Errorf("%w: %q (expected one of %s)", sentinel, name, strings.Join(names, ", "))
}

func nameOf[T ~uint8](v T, names []string) string {
	if v < 1 || int(v) > len(names) {
		return ""
	}
	return names[v-1]
}
