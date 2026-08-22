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

package resolution

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Rung is one archive of a Whisper ladder: queries whose `now - from` is at
// most MaxAge (and greater than the previous rung's MaxAge) are served at
// Step. MaxAge is the archive's retention, always a multiple of Step.
type Rung struct {
	MaxAge time.Duration `json:"max_age"`
	Step   time.Duration `json:"step"`
}

// State is a ladder's completeness
type State uint8

const (
	// StateUnknown: nothing is known
	StateUnknown State = iota
	// StatePartial: only individual (age, step) observations are known
	StatePartial
	// StateComplete: every rung and the maxRetention are known
	StateComplete
)

func (s State) String() string {
	switch s {
	case StatePartial:
		return "partial"
	case StateComplete:
		return "complete"
	}
	return "unknown"
}

// Observation is one measured (age, step) pair, from a probe or a captured
// response
type Observation struct {
	Age  time.Duration `json:"age"`
	Step time.Duration `json:"step"`
}

// Ladder is a metric's archive ladder. A complete ladder answers any age;
// a partial ladder answers only ages that lie between two observations
// with the same step, which is sound because rungs are monotonic in age.
type Ladder struct {
	Rungs        []Rung        `json:"rungs,omitempty"`
	Observations []Observation `json:"observations,omitempty"`
	State        State         `json:"state"`
	fp           string
}

// NewLadder builds a complete ladder from rungs, validating Whisper's rules:
// at least one rung, strictly increasing MaxAge and Step, each Step a
// multiple of the previous, and each MaxAge a multiple of its Step.
func NewLadder(rungs []Rung) (*Ladder, error) {
	if len(rungs) == 0 {
		return nil, fmt.Errorf("%w: no rungs", ErrInvalidLadder)
	}
	r := slices.Clone(rungs)
	slices.SortFunc(r, func(a, b Rung) int { return int(a.MaxAge - b.MaxAge) })
	for i, x := range r {
		if x.Step <= 0 || x.MaxAge <= 0 || x.MaxAge%x.Step != 0 {
			return nil, fmt.Errorf("%w: rung %d (%v:%v)", ErrInvalidLadder, i, x.Step, x.MaxAge)
		}
		if i > 0 {
			p := r[i-1]
			if x.MaxAge <= p.MaxAge || x.Step <= p.Step || x.Step%p.Step != 0 {
				return nil, fmt.Errorf("%w: rung %d (%v:%v) does not follow (%v:%v)",
					ErrInvalidLadder, i, x.Step, x.MaxAge, p.Step, p.MaxAge)
			}
		}
	}
	return &Ladder{Rungs: r, State: StateComplete}, nil
}

// NewPartial returns an empty partial ladder
func NewPartial() *Ladder {
	return &Ladder{State: StatePartial}
}

// Observe records an (age, step) measurement on a partial ladder. It
// returns ErrInconsistent when the observation contradicts an existing one
// (a different step at the same age, or a step that decreases with age).
func (l *Ladder) Observe(age, step time.Duration) error {
	if l.State == StateComplete {
		if s, _ := l.StepFor(age); s != step {
			return fmt.Errorf("%w: complete ladder predicts %v at age %v, observed %v",
				ErrInconsistent, s, age, step)
		}
		return nil
	}
	l.State = StatePartial
	i, found := slices.BinarySearchFunc(l.Observations, age,
		func(o Observation, a time.Duration) int { return int(o.Age - a) })
	if found {
		if l.Observations[i].Step != step {
			return fmt.Errorf("%w: age %v observed at both %v and %v",
				ErrInconsistent, age, l.Observations[i].Step, step)
		}
		return nil
	}
	if i > 0 && l.Observations[i-1].Step > step {
		return fmt.Errorf("%w: step %v at age %v is finer than %v at younger age %v",
			ErrInconsistent, step, age, l.Observations[i-1].Step, l.Observations[i-1].Age)
	}
	if i < len(l.Observations) && l.Observations[i].Step < step {
		return fmt.Errorf("%w: step %v at age %v is coarser than %v at older age %v",
			ErrInconsistent, step, age, l.Observations[i].Step, l.Observations[i].Age)
	}
	l.Observations = slices.Insert(l.Observations, i, Observation{Age: age, Step: step})
	return nil
}

// StepFor returns the step a query with the given `now - from` age receives.
// A complete ladder saturates at its coarsest rung beyond maxRetention, as
// Whisper clamps `from`. A partial ladder answers only when the age is
// bracketed by observations with the same step.
func (l *Ladder) StepFor(age time.Duration) (time.Duration, bool) {
	switch l.State {
	case StateComplete:
		for _, r := range l.Rungs {
			if age <= r.MaxAge {
				return r.Step, true
			}
		}
		return l.Rungs[len(l.Rungs)-1].Step, true
	case StatePartial:
		i, found := slices.BinarySearchFunc(l.Observations, age,
			func(o Observation, a time.Duration) int { return int(o.Age - a) })
		if found {
			return l.Observations[i].Step, true
		}
		if i > 0 && i < len(l.Observations) && l.Observations[i-1].Step == l.Observations[i].Step {
			return l.Observations[i].Step, true
		}
		if i == 0 && len(l.Observations) > 0 && age > 0 {
			// younger than the youngest observation: the finest rung
			// observed covers everything younger, by monotonicity
			return l.Observations[0].Step, true
		}
	}
	return 0, false
}

// MaxRetention is the oldest age the ladder holds data for (0 if unknown)
func (l *Ladder) MaxRetention() time.Duration {
	if l.State != StateComplete {
		return 0
	}
	return l.Rungs[len(l.Rungs)-1].MaxAge
}

// Saturates reports whether a query of this age is clamped to maxRetention
func (l *Ladder) Saturates(age time.Duration) bool {
	return l.State == StateComplete && age > l.MaxRetention()
}

// Fingerprint is a stable hash of a complete ladder's rungs; empty for a
// partial ladder. Equal ladders on different metrics share one fingerprint,
// which is what keeps the registry small (design note §5.6).
func (l *Ladder) Fingerprint() string {
	if l.State != StateComplete {
		return ""
	}
	if l.fp == "" {
		h := sha1.New()
		h.Write([]byte(l.String()))
		l.fp = hex.EncodeToString(h.Sum(nil))[:16]
	}
	return l.fp
}

// String renders a complete ladder in storage-schemas.conf retention syntax
// (step:retention, ...) and a partial one as its observations
func (l *Ladder) String() string {
	if l == nil {
		return "<nil>"
	}
	var b strings.Builder
	if l.State == StateComplete {
		for i, r := range l.Rungs {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(formatDur(r.Step))
			b.WriteByte(':')
			b.WriteString(formatDur(r.MaxAge))
		}
		return b.String()
	}
	b.WriteString("partial[")
	for i, o := range l.Observations {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(formatDur(o.Age))
		b.WriteString("->")
		b.WriteString(formatDur(o.Step))
	}
	b.WriteByte(']')
	return b.String()
}

// Clone returns a deep copy
func (l *Ladder) Clone() *Ladder {
	return &Ladder{
		Rungs: slices.Clone(l.Rungs), Observations: slices.Clone(l.Observations),
		State: l.State, fp: l.fp,
	}
}

// formatDur renders a duration in the largest whole Whisper unit
func formatDur(d time.Duration) string {
	s := int64(d / time.Second)
	for _, u := range [...]struct {
		n int64
		c byte
	}{{365 * 86400, 'y'}, {7 * 86400, 'w'}, {86400, 'd'}, {3600, 'h'}, {60, 'm'}} {
		if s >= u.n && s%u.n == 0 {
			return strconv.FormatInt(s/u.n, 10) + string(u.c)
		}
	}
	return strconv.FormatInt(s, 10) + "s"
}

// ParseRetentions parses a storage-schemas.conf retention list such as
// "10s:6h,1m:7d,10m:5y" into a complete ladder, following whisper's
// parseRetentionDef: precision[unit]:points-or-duration, units matched as
// prefixes of seconds/minutes/hours/days/weeks/years (so "m" is minutes).
func ParseRetentions(s string) (*Ladder, error) {
	var rungs []Rung
	for def := range strings.SplitSeq(s, ",") {
		def = strings.TrimSpace(def)
		prec, rest, ok := strings.Cut(def, ":")
		if !ok {
			return nil, fmt.Errorf("%w: retention %q lacks ':'", ErrInvalidLadder, def)
		}
		step, err := whisperDuration(strings.TrimSpace(prec))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidLadder, err)
		}
		rest = strings.TrimSpace(rest)
		var maxAge time.Duration
		if isAllDigits(rest) {
			points, err := strconv.ParseInt(rest, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvalidLadder, err)
			}
			maxAge = step * time.Duration(points)
		} else {
			d, err := whisperDuration(rest)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrInvalidLadder, err)
			}
			// whisper: points = retention // precision
			maxAge = step * (d / step)
		}
		rungs = append(rungs, Rung{MaxAge: maxAge, Step: step})
	}
	return NewLadder(rungs)
}

var whisperUnits = [...]struct {
	name string
	secs int64
}{{"seconds", 1}, {"minutes", 60}, {"hours", 3600}, {"days", 86400}, {"weeks", 604800}, {"years", 31536000}}

// whisperDuration parses "10s", "6h", "1m" (minutes), "90" (seconds)
func whisperDuration(s string) (time.Duration, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToLower(s[i:])
	for _, u := range whisperUnits {
		if strings.HasPrefix(u.name, unit) {
			if n > (1<<62)/u.secs/int64(time.Second) {
				return 0, fmt.Errorf("duration %q out of range", s)
			}
			return time.Duration(n*u.secs) * time.Second, nil
		}
	}
	return 0, fmt.Errorf("invalid duration unit %q", unit)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// LCM returns the least common multiple of a set of steps, which is the
// step graphite-web normalizes a mixed-step series list to
func LCM(steps []time.Duration) time.Duration {
	var l time.Duration
	for _, s := range steps {
		if s <= 0 {
			continue
		}
		if l == 0 {
			l = s
			continue
		}
		l = l / gcd(l, s) * s
	}
	return l
}

func gcd(a, b time.Duration) time.Duration {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
