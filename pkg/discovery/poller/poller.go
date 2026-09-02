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

// Package poller provides the shared, transport-neutral polling loop used
// by autodiscovery providers and by backend health checks: jittered start,
// an immediate first iteration, a per-iteration cadence chosen by the
// source, cancel-safe Start/Stop, and panic isolation so that one bad
// iteration cannot silently freeze the loop.
//
// The poller owns "call this, wait this long, don't die on a panic" and
// nothing else. Failure policy stays with the caller, because the two
// in-tree callers want opposite things: health checks change state after N
// consecutive failures, while discovery providers must hold their
// last-good membership on error and say nothing. Both express that inside
// their Source.
//
// Transport belongs to the Source too. pkg/discovery/poller/http is the
// outbound-HTTP implementation; DNS resolution, filesystem stats and
// future gRPC streams implement the same interface without this package
// knowing about any of them.
//
// # Layering
//
// The poller sits below both of its consumers: pkg/backends/healthcheck
// imports it, and so does every autodiscovery provider. It must therefore
// never reach back to pkg/backends/healthcheck or to the pkg/discovery root
// package, which TestPollerDoesNotDependOnItsConsumers pins.
//
// That is narrower than "must not reach pkg/backends at all", which no
// package that logs can satisfy: pkg/observability/logging imports
// pkg/config, which pulls in pkg/backends/options. It is harmless here only
// because pkg/backends/options does not import pkg/backends/healthcheck.
package poller

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

var (
	// ErrNilSource is returned by New when no Source is provided
	ErrNilSource = errors.New("poller requires a non-nil source")
	// ErrInvalidInterval is returned by New when the configured interval is
	// not positive. A zero interval is rejected rather than treated as
	// "never poll": callers that register something without polling it
	// should not construct a Poller at all.
	ErrInvalidInterval = errors.New("poller interval must be greater than zero")
	// ErrSourcePanic is the error an iteration is recorded as when the
	// Source panics. It is reported to the loop's failure accounting like
	// any other error, so a persistently panicking source backs off rather
	// than spinning.
	ErrSourcePanic = errors.New("poll source panicked")
)

// PollNow may be returned by a Source as its next wait to request another
// iteration immediately, rather than after the configured Interval. It is
// how blocking-query sources (Consul, Nomad) express "the server already
// did the waiting; re-issue now". A Source that returns PollNow without
// actually blocking will spin: the blocking is the Source's obligation.
const PollNow = time.Duration(-1)

// DefaultJitter is the startup jitter applied when Options.Jitter is zero.
// Jitter keeps a fleet of pollers constructed at the same instant (every
// backend at startup, every subscription after a config reload) from
// aligning their requests on the same upstream.
const DefaultJitter = time.Second

// Source performs one poll iteration.
//
// The returned next is the wait before the next iteration: zero means "use
// the poller's configured Interval", PollNow means "immediately", and any
// positive value overrides the interval for this one iteration (a DNS
// answer's TTL floor, a server-advertised retry-after).
//
// Returning an error does not stop the loop and does not change any
// caller-visible state; the poller applies its backoff and carries on. A
// Source that needs failures to mean something must implement that itself.
type Source interface {
	Poll(ctx context.Context) (next time.Duration, err error)
}

// Func adapts a plain function to Source.
type Func func(ctx context.Context) (time.Duration, error)

// Poll implements Source.
func (f Func) Poll(ctx context.Context) (time.Duration, error) {
	return f(ctx)
}

// Options configures a Poller.
type Options struct {
	// Name identifies the poller in logs and in the default panic handler
	Name string
	// Interval is the default wait between iterations, used whenever the
	// Source does not name its own. Must be greater than zero.
	Interval time.Duration
	// Timeout bounds a single iteration by deadlining its context. Zero
	// means no deadline. Sources must derive any transport deadline from
	// the iteration context rather than holding a second, independent one,
	// so that a blocking-query source setting Timeout to wait+slack is not
	// truncated by a shorter client timeout underneath it.
	Timeout time.Duration
	// Jitter is the maximum startup delay before the first iteration.
	// Zero selects DefaultJitter; a negative value disables jitter, which
	// tests want and production generally does not.
	Jitter time.Duration
	// MaxBackoff caps the exponential backoff applied after consecutive
	// failed iterations. Zero disables backoff, holding the loop at
	// Interval regardless of failures.
	MaxBackoff time.Duration
	// DetachIterations runs each iteration on a context detached from the
	// loop's, so that Stop waits for an in-flight iteration to finish
	// instead of cancelling it. Health checks need this to serialize a
	// re-registration over the probe it replaces. Discovery providers do
	// not, and must leave it false: a detached blocking query would hold
	// shutdown for the length of its server-side wait.
	DetachIterations bool
	// OnPanic receives recovered panics from the Source. When nil, panics
	// are recovered and logged with the poller's name. Callers wanting a
	// metric alongside the log supply their own handler.
	OnPanic safego.PanicHandler
}

// Poller runs a Source on a loop. Its zero value is not usable; construct
// one with New.
type Poller struct {
	name       string
	interval   time.Duration
	timeout    time.Duration
	jitter     time.Duration
	maxBackoff time.Duration
	detach     bool
	onPanic    safego.PanicHandler
	src        Source
	// trigger is buffered to one so concurrent Trigger calls coalesce into
	// a single extra iteration instead of queueing
	trigger chan struct{}
	// mtx guards cancel and wg across Start/Stop cycles
	mtx    sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New returns a Poller that runs src according to o. It returns an error
// rather than silently correcting an unusable configuration.
func New(o Options, src Source) (*Poller, error) {
	if src == nil {
		return nil, ErrNilSource
	}
	if o.Interval <= 0 {
		return nil, ErrInvalidInterval
	}
	jitter := o.Jitter
	switch {
	case jitter < 0:
		jitter = 0
	case jitter == 0:
		jitter = DefaultJitter
	}
	p := &Poller{
		name:       o.Name,
		interval:   o.Interval,
		timeout:    o.Timeout,
		jitter:     jitter,
		maxBackoff: o.MaxBackoff,
		detach:     o.DetachIterations,
		onPanic:    o.OnPanic,
		src:        src,
		trigger:    make(chan struct{}, 1),
	}
	if p.onPanic == nil {
		p.onPanic = p.logPanic
	}
	return p, nil
}

// Name returns the poller's configured name.
func (p *Poller) Name() string {
	return p.name
}

// Start begins the loop, bounded by ctx. Cancelling ctx is equivalent to
// Stop. Start on an already-running Poller stops the previous loop and
// waits for it to exit before starting the new one, so a caller may
// restart a Poller without leaking its predecessor.
func (p *Poller) Start(ctx context.Context) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.cancel != nil {
		p.cancel()
		p.wg.Wait()
		p.cancel = nil
	}
	p.wg = sync.WaitGroup{}
	loopCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Go(func() { p.run(loopCtx) })
}

// Stop ends the loop and waits for it to exit. It is idempotent, and safe
// to call on a Poller that was never started. Whether Stop waits for an
// in-flight iteration or cancels it is set by Options.DetachIterations.
func (p *Poller) Stop() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.cancel == nil {
		return
	}
	p.cancel()
	p.wg.Wait()
	p.cancel = nil
}

// Trigger requests an iteration immediately rather than waiting out the
// current interval, for out-of-band refreshes such as a filesystem change
// notification arriving between polls.
//
// Triggers coalesce, but into at most two iterations rather than one: a
// burst arriving while an iteration is in flight is answered by that
// iteration and by one more after it, because a change observed mid-poll
// may not be reflected in the result the poll is about to return. A call
// against a stopped Poller is a no-op, and one raised before Start is
// discarded by the immediate first iteration.
func (p *Poller) Trigger() {
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

// run is the loop itself: jitter, then iterate until the context ends.
func (p *Poller) run(ctx context.Context) {
	if !p.waitJitter(ctx) {
		return
	}
	// discard any trigger raised while stopped or during jitter: the
	// immediate first iteration below already satisfies it, and letting it
	// stand would spend a second iteration answering the same request
	select {
	case <-p.trigger:
	default:
	}
	// fire the first iteration immediately; jitter has already spread the
	// fleet out, and a caller that just started a poller wants an answer
	timer := time.NewTimer(0)
	defer timer.Stop()
	var failures int
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-p.trigger:
			// the timer is still armed for the iteration this trigger is
			// preempting; stop and drain it so the Reset below is clean
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		next, err := p.poll(ctx)
		if ctx.Err() != nil {
			// stopped mid-iteration; the result is no longer wanted
			return
		}
		wait := p.interval
		switch {
		case err != nil:
			failures++
			if p.maxBackoff > 0 {
				wait = backoff(p.interval, failures, p.maxBackoff)
			}
		default:
			failures = 0
			switch {
			case next == PollNow:
				wait = 0
			case next > 0:
				wait = next
			}
		}
		timer.Reset(wait)
	}
}

// waitJitter sleeps out the startup jitter, reporting false if the poller
// was stopped before it elapsed.
func (p *Poller) waitJitter(ctx context.Context) bool {
	if p.jitter <= 0 {
		return true
	}
	t := time.NewTimer(randomDuration(p.jitter))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// poll runs one iteration under the configured detachment and timeout,
// converting a panic into ErrSourcePanic so the loop survives it.
func (p *Poller) poll(ctx context.Context) (time.Duration, error) {
	ictx := ctx
	if p.detach {
		ictx = context.WithoutCancel(ctx)
	}
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ictx, cancel = context.WithTimeout(ictx, p.timeout)
		defer cancel()
	}
	var next time.Duration
	var err error
	safego.Run(func(r any, stack []byte) {
		p.onPanic(r, stack)
		// the assignment below never ran, so name the failure explicitly
		// rather than reporting a successful zero-wait iteration
		next, err = 0, ErrSourcePanic
	}, func() {
		next, err = p.src.Poll(ictx)
	})
	return next, err
}

// logPanic is the default OnPanic handler.
func (p *Poller) logPanic(r any, stack []byte) {
	logger.Error("poll source panic; loop continuing",
		logging.Pairs{
			keys.Name:  p.name,
			keys.Panic: fmt.Sprintf("%v", r),
			keys.Stack: string(stack),
		})
}

// backoff returns base doubled once per consecutive failure beyond the
// first, clamped to ceiling. It never overflows: the doubling stops as
// soon as another would pass the ceiling.
func backoff(base time.Duration, failures int, ceiling time.Duration) time.Duration {
	if base <= 0 {
		return ceiling
	}
	d := base
	for range max(failures-1, 0) {
		if d > ceiling/2 {
			return ceiling
		}
		d *= 2
	}
	return min(d, ceiling)
}

// randomDuration returns a uniform duration in [0, d).
func randomDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d)))
	if err != nil {
		return d
	}
	return time.Duration(n.Int64())
}
