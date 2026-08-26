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
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"

	"go.opentelemetry.io/otel/attribute"
)

// Learner discovers a leaf's complete archive ladder with probes and writes it
// to the registry. Runs are deduplicated per leaf and never block a request.
type Learner struct {
	Prober   *Prober
	Expander *Expander
	Registry *Registry
	Observer Observer
	// Concurrency caps simultaneous learning runs; Budget caps the probes
	// one run may issue
	Concurrency int
	Budget      int
	// Name labels log lines with the backend
	Name string
	// Tracers carries the backend's tracer, when a request has published one
	Tracers *Tracers
	// Now is the clock (tests override it)
	Now func() time.Time

	mu       sync.Mutex
	inflight map[string]struct{}
	active   atomic.Int32
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// sweepFactor is the ratio between successive ages in the discovery sweep
const sweepFactor = 4

// minAge is the youngest age probed; sweepStart is where the geometric sweep
// begins. A rung shorter than sweepStart is found by the boundary search.
const (
	minAge     = time.Second
	sweepStart = 2 * time.Minute
)

// farPast is an age no Whisper retention reaches, used to discover
// maxRetention; a probe's from never precedes the epoch (graphite-web rejects it).
const farPast = 100 * 365 * 24 * time.Hour

func (r *learnRun) farPastAge() time.Duration {
	return min(farPast, r.now.Sub(time.Unix(0, 0)))
}

func (l *Learner) init() {
	l.mu.Lock()
	if l.inflight == nil {
		l.inflight = make(map[string]struct{})
		l.ctx, l.cancel = context.WithCancel(context.Background())
	}
	l.mu.Unlock()
}

func (l *Learner) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Close cancels in-flight runs and waits for them
func (l *Learner) Close() {
	l.init()
	l.cancel()
	l.wg.Wait()
}

// Wait blocks until every scheduled run has finished
func (l *Learner) Wait() {
	l.wg.Wait()
}

// Schedule starts learning a leaf in the background, confirming a non-nil hint
// first; false when already in flight, over the cap, or Concurrency is negative.
func (l *Learner) Schedule(leaf string, hint *Ladder) bool {
	if l.Concurrency < 0 {
		return false
	}
	l.init()
	l.mu.Lock()
	if _, ok := l.inflight[leaf]; ok || (l.Concurrency > 0 && int(l.active.Load()) >= l.Concurrency) {
		l.mu.Unlock()
		return false
	}
	l.inflight[leaf] = struct{}{}
	l.active.Add(1)
	l.mu.Unlock()
	l.wg.Add(1)
	safego.Go(func(r any, stack []byte) {
		logger.Error("graphite ladder learning panicked", logging.Pairs{
			"backendName": l.Name, "leaf": leaf, "panic": fmt.Sprint(r), "stack": string(stack),
		})
	}, func() {
		defer l.wg.Done()
		defer func() {
			l.mu.Lock()
			delete(l.inflight, leaf)
			l.mu.Unlock()
			l.active.Add(-1)
		}()
		_, _ = l.Learn(l.ctx, leaf, hint)
	})
	return true
}

// Learn discovers the ladder of one leaf synchronously and records it. On
// failure the leaf is negative-cached and any partial ladder is still recorded.
func (l *Learner) Learn(ctx context.Context, leaf string, hint *Ladder) (*Ladder, error) {
	l.init()
	start := l.now()
	ctx, span := tspan.NewChildSpan(ctx, l.Tracers.Get(), "GraphiteLearnLadder")
	if span != nil {
		defer span.End()
	}
	run := &learnRun{l: l, ctx: ctx, leaf: leaf, now: start.Truncate(time.Second), partial: NewPartial()}
	var ladder *Ladder
	var err error
	if hint != nil && hint.State == StateComplete {
		if ok, cerr := run.confirm(hint); ok {
			ladder = hint.Clone()
		} else if cerr != nil && !errors.Is(cerr, ErrInconsistent) {
			err = cerr
		} else {
			logger.Info("graphite static ladder contradicted by origin; learning",
				logging.Pairs{"backendName": l.Name, "leaf": leaf, "static": hint.String()})
		}
	}
	// a deployment has a handful of ladders: before discovering from
	// scratch, try to confirm the ones already known (most used first)
	for _, known := range l.Registry.KnownLadders() {
		if ladder != nil || err != nil {
			break
		}
		if hint != nil && known.Fingerprint() == hint.Fingerprint() {
			continue
		}
		run.partial = NewPartial() // observations from a failed confirm may mislead the next
		ok, cerr := run.confirm(known)
		switch {
		case ok:
			ladder = known.Clone()
		case cerr != nil && !errors.Is(cerr, ErrInconsistent):
			err = cerr
		}
	}
	if ladder == nil && err == nil {
		run.partial = NewPartial()
		ladder, err = run.discover()
	}
	if err != nil {
		if len(run.partial.Observations) > 0 {
			if key, perr := l.Registry.SetLadder(leaf, run.partial); perr == nil {
				_ = l.Registry.SetLeaf(leaf, key, Exact)
			}
		}
		backoff := l.Registry.SetNegative(leaf)
		logger.Warn("graphite ladder learning failed; negative-cached", logging.Pairs{
			"backendName": l.Name,
			"leaf":        leaf, "probes": run.probes, "error": err.Error(), "negativeCacheBackoff": backoff.String(),
			"partial": run.partial.String(),
		})
		return run.partial, err
	}
	key, perr := l.Registry.SetLadder(leaf, ladder)
	if perr != nil {
		return nil, perr
	}
	if perr = l.Registry.SetLeaf(leaf, key, Exact); perr != nil {
		return nil, perr
	}
	l.Registry.ClearNegative(leaf)
	if l.Observer != nil {
		l.Observer.Ladders(l.Registry.Stats().CompleteLadders)
	}
	logger.Debug("graphite ladder learned", logging.Pairs{
		"backendName": l.Name, "leaf": leaf,
		"ladder": ladder.String(), "fingerprint": key, "probes": run.probes,
		"duration": l.now().Sub(start).String(),
	})
	tspan.SetAttributes(l.Tracers.Get(), span,
		attribute.String("graphite.ladder", ladder.String()),
		attribute.Int("graphite.probes", run.probes),
	)
	return ladder, nil
}

// learnRun is the state of one learning run
type learnRun struct {
	l       *Learner
	ctx     context.Context
	leaf    string
	now     time.Time
	probes  int
	partial *Ladder
}

// probes the step at an age; ok is false beyond maxRetention, and every
// successful probe is recorded as an observation
func (r *learnRun) step(age time.Duration) (time.Duration, bool, error) {
	if r.l.Budget > 0 && r.probes >= r.l.Budget {
		return 0, false, ErrProbeBudget
	}
	if err := r.ctx.Err(); err != nil {
		return 0, false, err
	}
	r.probes++
	res := r.l.Prober.Narrow(r.ctx, r.leaf, age, r.now)
	switch res.Result {
	case ResultError:
		return 0, false, res.Err
	case ResultEmpty:
		return 0, false, nil
	}
	if err := r.partial.Observe(age, res.Step); err != nil {
		return 0, false, err
	}
	return res.Step, true, nil
}

// checks a ladder against the origin with 2n+3 probes: the finest step, both
// sides of every rung boundary and of maxRetention; ok false on disagreement
func (r *learnRun) confirm(h *Ladder) (bool, error) {
	s, ok, err := r.step(minAge)
	if err != nil || !ok || s != h.Rungs[0].Step {
		return false, orInconsistent(err, ok)
	}
	for i, rung := range h.Rungs {
		s, ok, err = r.step(rung.MaxAge)
		if err != nil || !ok || s != rung.Step {
			return false, orInconsistent(err, ok)
		}
		if i+1 < len(h.Rungs) {
			s, ok, err = r.step(rung.MaxAge + time.Second)
			if err != nil || !ok || s != h.Rungs[i+1].Step {
				return false, orInconsistent(err, ok)
			}
		}
	}
	// beyond maxRetention the narrow window must be empty
	_, ok, err = r.step(h.MaxRetention() + 2*time.Second)
	if err != nil {
		return false, err
	}
	return !ok, nil
}

func orInconsistent(err error, ok bool) error {
	if err != nil {
		return err
	}
	_ = ok
	return ErrInconsistent
}

// learns the ladder from scratch: the finest step at minAge, a geometric sweep
// that brackets maxRetention, then binary searches for the edge and boundaries
func (r *learnRun) discover() (*Ladder, error) {
	s0, ok, err := r.step(minAge)
	if err != nil {
		return nil, err
	}
	if !ok {
		switch r.l.Expander.Exists(r.ctx, r.leaf) {
		case NotExists:
			return nil, ErrMissingMetric
		case Exists:
			return nil, fmt.Errorf("%w: existing metric returned nothing at age %v", ErrInconsistent, minAge)
		}
		return nil, errors.New("existence check failed")
	}
	type obs struct {
		age, step time.Duration
	}
	sweep := []obs{{minAge, s0}}
	limit := r.farPastAge()
	var firstOut time.Duration
	for age := sweepStart; ; age *= sweepFactor {
		if age > limit {
			age = limit
		}
		s, ok, err := r.step(age)
		if err != nil {
			return nil, err
		}
		if !ok {
			firstOut = age
			break
		}
		sweep = append(sweep, obs{age, s})
		if age == limit {
			return nil, fmt.Errorf("%w: data older than %v", ErrInconsistent, limit)
		}
	}
	lastIn := sweep[len(sweep)-1]
	maxRet, err := r.maxRetention(lastIn.age, firstOut, lastIn.step)
	if err != nil {
		return nil, err
	}
	sweep = append(sweep, obs{maxRet, lastIn.step})
	// boundaries
	var rungs []Rung
	for i := 0; i+1 < len(sweep); i++ {
		lo, hi := sweep[i], sweep[i+1]
		for lo.step != hi.step {
			if hi.step < lo.step || hi.step%lo.step != 0 {
				return nil, fmt.Errorf("%w: step %v at %v then %v at %v", ErrInconsistent,
					lo.step, lo.age, hi.step, hi.age)
			}
			boundary, next, err := r.boundary(lo.age, hi.age, lo.step)
			if err != nil {
				return nil, err
			}
			rungs = append(rungs, Rung{MaxAge: boundary, Step: lo.step})
			lo = obs{boundary + lo.step, next}
		}
	}
	rungs = append(rungs, Rung{MaxAge: maxRet, Step: lastIn.step})
	return NewLadder(rungs)
}

// finds the oldest age held, known to lie in [inside, outside): a wide probe's
// clamped start proposes a candidate, confirmed from both sides, else binary
// search. This is the one probe that sends maxDataPoints (=2): only its start
// is used, and the alternative is reading the whole coarsest archive.
func (r *learnRun) maxRetention(inside, outside, coarse time.Duration) (time.Duration, error) {
	if r.l.Budget > 0 && r.probes >= r.l.Budget {
		return 0, ErrProbeBudget
	}
	r.probes++
	res := r.l.Prober.Wide(r.ctx, r.leaf, r.farPastAge(), r.now, url.Values{"maxDataPoints": {"2"}})
	if res.Result == ResultError {
		return 0, res.Err
	}
	if res.Result == ResultStep {
		approx := r.now.Sub(res.Start)
		candidate := (approx/coarse)*coarse + coarse
		if candidate >= inside && candidate < outside {
			in, err := r.inside(candidate)
			if err != nil {
				return 0, err
			}
			if in {
				out, err := r.inside(candidate + 2*time.Second)
				if err != nil {
					return 0, err
				}
				if !out {
					return candidate, nil
				}
				inside = candidate
			} else {
				outside = candidate
			}
		}
	}
	return r.searchEdge(inside, outside, coarse)
}

func (r *learnRun) inside(age time.Duration) (bool, error) {
	_, ok, err := r.step(age)
	return ok, err
}

// binary-searches maxRetention on multiples of the coarse step between an age
// known to have data and one known not to
func (r *learnRun) searchEdge(inside, outside, step time.Duration) (time.Duration, error) {
	lo, hi := inside/step, (outside+step-1)/step // lo*step <= inside has data; hi*step >= outside has none
	if hi <= lo {
		hi = lo + 1
	}
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		in, err := r.inside(mid * step)
		if err != nil {
			return 0, err
		}
		if in {
			lo = mid
		} else {
			hi = mid
		}
	}
	maxRet := lo * step
	if maxRet < inside {
		// inside was not a multiple of step: the edge is the next multiple
		maxRet += step
	}
	out, err := r.inside(maxRet + 2*time.Second)
	if err != nil {
		return 0, err
	}
	if out {
		return 0, fmt.Errorf("%w: retention edge not found near %v", ErrInconsistent, maxRet)
	}
	return maxRet, nil
}

// finds the largest multiple of fine in (lo, hi] still served at the fine step
// (the rung's MaxAge), plus the step one fine step beyond it (the next rung's)
func (r *learnRun) boundary(lo, hi, fine time.Duration) (time.Duration, time.Duration, error) {
	lk, hk := lo/fine, hi/fine+1 // lk*fine <= lo is fine-stepped; hk*fine > hi is coarser
	var next time.Duration
	for hk-lk > 1 {
		mid := (lk + hk) / 2
		s, ok, err := r.step(mid * fine)
		if err != nil {
			return 0, 0, err
		}
		if !ok {
			return 0, 0, fmt.Errorf("%w: empty at age %v during boundary search", ErrInconsistent, mid*fine)
		}
		if s == fine {
			lk = mid
		} else {
			hk, next = mid, s
		}
	}
	if next == 0 {
		s, ok, err := r.step(hk * fine)
		if err != nil {
			return 0, 0, err
		}
		if !ok || s == fine {
			return 0, 0, fmt.Errorf("%w: no coarser step beyond %v", ErrInconsistent, lk*fine)
		}
		next = s
	}
	return lk * fine, next, nil
}
