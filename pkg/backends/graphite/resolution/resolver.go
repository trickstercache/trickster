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
	"context"
	"time"
)

// Resolution is the resolver's answer for one target
type Resolution struct {
	Step       time.Duration
	Confidence Confidence
	Source     string
	// Leaves are the resolved leaf paths, sorted; ExpansionID is their
	// token for the cache key (changes when a wildcard's expansion changes)
	Leaves      []string
	ExpansionID string
	// MaxRetention is the shortest maxRetention among the leaves' complete
	// ladders (0 when any is unknown); queries older than it are clamped
	MaxRetention time.Duration
	// LadderKeys are the registry keys of the leaves' ladders, in Leaves
	// order, for cache-key composition
	LadderKeys []string
	// Reason is the frozen fallback reason when Confidence is Unknown
	Reason string
}

// Resolver implements the ordered strategy chain of design note §6.1
type Resolver struct {
	Registry *Registry
	Expander *Expander
	Learner  *Learner
	Static   *Static
	Observer Observer
}

// frozen fallback reasons (parsing.Reason*, repeated here to avoid the
// import; the values are identical and tested to be)
const (
	reasonUnknownStep   = "unknown_step"
	reasonMissingTarget = "missing_target"
)

// Resolve predicts the step for a target whose leaf path expressions are
// leafExprs, queried at the given `now - from` age. fixedStep is non-zero
// when the target's function chain fixes the output step (summarize), in
// which case the step is Derived and the leaves are resolved only for
// their retention. normalized is true when the target wraps its leaves in
// a function: graphite-web then normalizes mixed steps to their LCM, while
// a bare path expression returns every series at its own native step,
// which one query cannot model, so mixed steps are Unknown there.
//
// Strategy: expand wildcards; look every leaf up in the registry (learned
// ladders answer Exact); fall back to the static layer (Configured,
// scheduling a confirming probe); schedule learning for anything still
// unknown and return Unknown. Nothing here blocks on the origin except
// wildcard expansion, which is cached.
func (r *Resolver) Resolve(ctx context.Context, leafExprs []string, fixedStep, age time.Duration,
	normalized bool,
) Resolution {
	res := Resolution{Confidence: Unknown, Source: SourceNone}
	var leaves []string
	var ids []string
	for _, expr := range leafExprs {
		l, id, err := r.Expander.Expand(ctx, expr)
		if err != nil {
			res.Reason = reasonUnknownStep
			r.observe(res)
			return res
		}
		leaves = append(leaves, l...)
		ids = append(ids, id)
	}
	if len(leaves) == 0 {
		res.Reason = reasonMissingTarget
		r.observe(res)
		return res
	}
	res.Leaves = leaves
	if len(ids) == 1 {
		res.ExpansionID = ids[0]
	} else {
		res.ExpansionID = ExpansionID(ids)
	}

	steps := make([]time.Duration, 0, len(leaves))
	res.LadderKeys = make([]string, 0, len(leaves))
	conf := Exact
	source := SourceRegistry
	allKnown := true
	for _, leaf := range leaves {
		if key, c, ok := r.Registry.Leaf(leaf); ok {
			if ladder, ok := r.Registry.Ladder(key); ok {
				if s, ok := ladder.StepFor(age); ok {
					steps = append(steps, s)
					res.LadderKeys = append(res.LadderKeys, key)
					r.retention(&res, ladder)
					if c == Configured {
						conf, source = Configured, SourceStatic
					}
					continue
				}
			}
		}
		// static seed: Configured now, confirmed in the background
		if hint, ok := r.Static.Match(leaf); ok {
			if s, ok := hint.StepFor(age); ok {
				steps = append(steps, s)
				res.LadderKeys = append(res.LadderKeys, hint.Fingerprint())
				r.retention(&res, hint)
				conf, source = Configured, SourceStatic
				if _, neg := r.Registry.Negative(leaf); !neg {
					r.Learner.Schedule(leaf, hint)
				}
				continue
			}
		}
		allKnown = false
		if _, neg := r.Registry.Negative(leaf); !neg {
			r.Learner.Schedule(leaf, nil)
		}
	}
	if !allKnown {
		res.Reason = reasonUnknownStep
		res.MaxRetention = 0
		r.observe(res)
		return res
	}
	if fixedStep > 0 {
		res.Step, res.Confidence, res.Source = fixedStep, Derived, SourceFunction
		if conf == Configured {
			res.Confidence = Configured
		}
		r.observe(res)
		return res
	}
	res.Step = LCM(steps)
	res.Confidence, res.Source = conf, source
	if len(leaves) > 1 {
		mixed := false
		for _, s := range steps {
			if s != res.Step {
				mixed = true
				break
			}
		}
		if mixed && !normalized {
			// bare wildcard across ladders: the origin returns each
			// series at its own step
			res.Step, res.Confidence, res.Source = 0, Unknown, SourceNone
			res.Reason = reasonUnknownStep
			r.observe(res)
			return res
		}
		if conf == Exact {
			// an LCM over several leaves is computed, not read from one
			// response
			res.Confidence = Derived
		}
	}
	r.observe(res)
	return res
}

// retention folds a ladder's maxRetention into the resolution: the
// shortest wins, and an unknown one (partial ladder) makes it unknown
func (r *Resolver) retention(res *Resolution, l *Ladder) {
	mr := l.MaxRetention()
	if mr == 0 {
		res.MaxRetention = -1
		return
	}
	if res.MaxRetention == 0 || (res.MaxRetention > 0 && mr < res.MaxRetention) {
		res.MaxRetention = mr
	}
}

// Observe records a step read from a captured origin response (decision D1,
// learn on first response) as a partial-ladder observation for each leaf,
// and schedules full discovery so that later requests at other ages can be
// answered. It is never speculative: the step came from the origin.
func (r *Resolver) Observe(leaves []string, age, step time.Duration) {
	if step <= 0 || len(leaves) != 1 {
		// a multi-leaf response reports the LCM, which says nothing certain
		// about any single leaf
		return
	}
	leaf := leaves[0]
	var ladder *Ladder
	if key, _, ok := r.Registry.Leaf(leaf); ok {
		if l, ok := r.Registry.Ladder(key); ok {
			ladder = l
		}
	}
	switch {
	case ladder != nil && ladder.State == StatePartial:
		ladder = ladder.Clone()
	case ladder != nil && ladder.State == StateComplete:
		if s, _ := ladder.StepFor(age); s == step {
			return
		}
		// the origin disagrees with a complete ladder: it changed
		r.Registry.BumpGeneration()
		ladder = NewPartial()
	default:
		ladder = NewPartial()
	}
	if err := ladder.Observe(age, step); err != nil {
		r.Registry.BumpGeneration()
		ladder = NewPartial()
		_ = ladder.Observe(age, step)
	}
	if key, err := r.Registry.SetLadder(leaf, ladder); err == nil {
		_ = r.Registry.SetLeaf(leaf, key, Exact)
	}
	r.Learner.Schedule(leaf, nil)
}

func (r *Resolver) observe(res Resolution) {
	if r.Observer == nil {
		return
	}
	r.Observer.Lookup(res.Confidence.String(), res.Source)
	st := r.Registry.Stats()
	r.Observer.RegistryEntries(LayerLeaf, st.Leaves)
	r.Observer.RegistryEntries(LayerLadder, st.Ladders)
	r.Observer.RegistryEntries(LayerTarget, st.Targets)
	r.Observer.RegistryEntries(LayerNegative, st.Negatives)
}
