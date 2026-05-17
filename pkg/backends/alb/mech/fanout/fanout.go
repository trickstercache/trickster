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

// Package fanout is the shared primitive for ALB mechanisms that scatter a
// single inbound request to N pool members and gather their responses. It
// centralizes the bug-prone parts of fanout that each mechanism otherwise
// re-implements: body priming, context propagation, capture-buffer bounding,
// panic recovery, concurrency limiting, and metric attribution.
//
// Mechanisms that wait for all members and decide afterwards (NLM, TSM) call
// All. Mechanisms that race for a first-qualifying response (FR/FGR) call
// WaitForFirst with a winner predicate; WaitForFirst cancels remaining work
// the moment a winner is claimed and returns that winner without waiting for
// slow losers to drain.
package fanout

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"golang.org/x/sync/errgroup"
)

// perSlotReserveBytes returns the worst-case per-slot capture reservation
// matching PrepareClone's effective per-writer cap. Keeping these in sync
// is required for the aggregate-cap admission to bound actual in-flight
// memory; otherwise a zero MaxCaptureBytes silently disables admission while
// each writer still allocates up to capture.DefaultMaxBytes.
func perSlotReserveBytes(cfg Config) int64 {
	if cfg.MaxCaptureBytes > 0 {
		return int64(cfg.MaxCaptureBytes)
	}
	return int64(capture.DefaultMaxBytes)
}

// reasonRoutingFlap labels a failure where the target was healthy at
// LiveTargets snapshot time but had flipped to Failing by the time the
// fanout goroutine observed its response. Operators alerting on
// fanout_failures_total can exclude this reason to avoid health-flap noise.
const reasonRoutingFlap = "routing_flap"

// failureReason returns reasonRoutingFlap if t's hcStatus is now below
// StatusPassing (i.e., the target was unhealthy at dispatch-observation
// time, indicating a snapshot/live-status race). Otherwise it returns the
// fallback reason supplied by the caller (e.g. "truncated"). Targets with
// no hcStatus fall through to the fallback.
func failureReason(t *pool.Target, fallback string) string {
	if t == nil {
		return fallback
	}
	st := t.HealthStatus()
	if st == nil {
		return fallback
	}
	if st.Get() < healthcheck.StatusPassing {
		return reasonRoutingFlap
	}
	return fallback
}

// Result holds one pool member's outcome from a fanout call. Results are
// returned slot-indexed: Result[i] corresponds to targets[i] from the
// original Targets slice. Slots whose target was nil have Failed == true.
type Result struct {
	// Index is the slot position in the original Targets slice.
	Index int
	// Request is the cloned request handed to the member's handler.
	// Mechanism code can read its Resources (e.g. TSM's MergeFunc /
	// MergeRespondFunc / TS) in post-gather inspection or via OnResult.
	Request *http.Request
	// Capture is the bounded response writer that captured the member's
	// reply. Nil if the slot failed before serving.
	Capture *capture.CaptureResponseWriter
	// Failed is true when the slot did not produce a usable response.
	// Reasons: clone error, panic in the member's handler, or capture
	// truncation (the upstream exceeded MaxCaptureBytes). Mechanism code
	// uses this to surface partial-failure signals.
	Failed bool
	// Err carries a clone or transport error, if any. A recovered panic
	// is reflected only in Failed; the panic value is logged + metered
	// inside the fanout goroutine.
	Err error
}

// Config configures one fanout call.
type Config struct {
	// Mechanism is the registered short name of the mechanism running the
	// fanout ("fr", "nlm", "tsm"). Used in panic recovery logs and as the
	// mechanism label on the fanout_failures_total metric.
	Mechanism types.Name
	// Variant carries optional sub-fanout context within a Mechanism, e.g.
	// "avg-sum" / "avg-count" for TSM's paired weighted-avg queries. Empty
	// for mechanisms with only one fanout path. Surfaces as the variant
	// label on the fanout_failures_total metric.
	Variant string
	// ConcurrencyLimit caps in-flight member calls. 0 means unlimited.
	ConcurrencyLimit int
	// MaxCaptureBytes caps each member's response body capture. 0 uses
	// capture.DefaultMaxBytes.
	MaxCaptureBytes int
	// MaxFanoutCaptureBytes, if > 0, caps the aggregate in-flight
	// capture-buffer reservations across all slots in one fanout call. Each
	// slot reserves the effective per-writer cap (cfg.MaxCaptureBytes, or
	// capture.DefaultMaxBytes when zero) as its worst case. Slots dispatched
	// after the budget would go negative are fail-fasted with Failed=true
	// and Capture=nil before the handler runs. Combined with the per-writer
	// hard cap inside capture.CaptureResponseWriter.Write, this bounds
	// aggregate in-flight capture memory at MaxFanoutCaptureBytes even when
	// upstreams return more bytes than declared by Content-Length. Defaults
	// to 0 (no aggregate cap).
	MaxFanoutCaptureBytes int
	// Resources, if non-nil, returns the Resources to attach to each
	// cloned request before the member's handler sees it. Nil resources
	// is a valid return value.
	Resources func(idx int) *request.Resources
	// Context, if non-nil, transforms the parent context.Context before
	// each clone receives it. NLM uses this to call tctx.ClearResources;
	// most mechanisms can leave it nil.
	Context func(parent context.Context) context.Context
	// OnResult, if non-nil, is called inside the fanout goroutine after
	// the member's handler returns and before the goroutine exits. Use
	// this for per-slot side effects that should run in parallel with
	// other in-flight members (e.g. TSM merges into a shared accumulator).
	// OnResult must be safe for concurrent invocation. The supplied
	// Result is the same one that will appear in the All return slice.
	OnResult func(idx int, r *Result)
}

// All scatters parent to every target and gathers slot-ordered Results.
//
// The caller MUST have primed parent's body (via PrimeBody or equivalent)
// before invoking All; otherwise concurrent CloneWithoutResources calls
// will race on r.Body / rsc.RequestBody for POST/PUT/PATCH requests.
//
// Each clone is given a per-slot Resources value (if Config.Resources is
// set), a derived context (Config.Context if set; ctx otherwise), and a
// capture.CaptureResponseWriter bounded by Config.MaxCaptureBytes.
//
// A panic in any member's handler is recovered, logged, and counted via
// the trickster_alb_fanout_failures_total metric; the slot's Failed field
// is set so the caller can surface partial-failure to its response.
//
// All returns when every spawned goroutine has finished. The returned
// slice has len(targets) entries; results[i].Index == i. The error is the
// first non-nil error from any goroutine (typically a clone failure); per-
// slot errors are also recorded in results[i].Err. The primitive logs +
// meters every failure regardless; callers can use the returned error to
// propagate through their own errgroup, render a fatal response, etc.
func All(ctx context.Context, parent *http.Request, targets pool.Targets, cfg Config) ([]Result, error) {
	return scatter(ctx, parent, targets, cfg, nil)
}

// WaitForFirst scatters parent to every target and returns the first slot
// whose Result satisfies predicate. Semantics are "first matching predicate,
// cancel rest, return the winner", which is distinct from errgroup-style
// "first to finish or error". Truncated captures are never eligible (they
// are disqualified inside the primitive).
//
// winnerIdx is the slot index of the winning result, or -1 if no result
// satisfied predicate. When winnerIdx == -1, results is fully gathered and
// slot-ordered exactly like All's return: callers can iterate it to implement
// their own fallback policy (e.g. FR's "first non-failed slot" pick when no
// member qualified). When winnerIdx >= 0, only results[winnerIdx] is
// guaranteed to be complete when WaitForFirst returns.
//
// Other than the early-cancel behaviour, WaitForFirst shares its clone,
// capture, resource, recovery, and metric machinery with All.
func WaitForFirst(ctx context.Context, parent *http.Request, targets pool.Targets, cfg Config, predicate func(*Result) bool) (winnerIdx int, results []Result, err error) {
	winnerIdx = -1
	if predicate == nil {
		results, err = scatter(ctx, parent, targets, cfg, nil)
		return winnerIdx, results, err
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	gathered := make([]Result, len(targets))

	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	claimed := -1
	scatterDone := false
	var scatterErr error
	var winnerResult Result

	onComplete := func(i int, r *Result) {
		if r.Failed || r.Capture == nil {
			return
		}
		if !predicate(r) {
			return
		}
		mu.Lock()
		if claimed == -1 {
			claimed = i
			winnerResult = *r
			cancel()
			cond.Broadcast()
		}
		mu.Unlock()
	}

	go func() {
		_, err := scatterInto(raceCtx, parent, targets, cfg, onComplete, gathered)
		mu.Lock()
		scatterErr = err
		scatterDone = true
		cond.Broadcast()
		mu.Unlock()
	}()

	stopWake := context.AfterFunc(ctx, func() {
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	})
	defer stopWake()

	mu.Lock()
	defer mu.Unlock()
	for {
		winnerIdx = claimed
		if winnerIdx >= 0 {
			results = make([]Result, len(targets))
			results[winnerIdx] = winnerResult
			return winnerIdx, results, nil
		}
		if scatterDone {
			return -1, gathered, scatterErr
		}
		if err := ctx.Err(); err != nil {
			cancel()
			return -1, make([]Result, len(targets)), err
		}
		cond.Wait()
	}
}

// scatter is the shared implementation behind All and WaitForFirst. perSlot,
// if non-nil, is called inside each fanout goroutine after the handler
// returns and the truncation check fires (but before cfg.OnResult). It is
// the race-pick hook: WaitForFirst uses it to CAS-claim the winner slot.
// perSlot must be safe for concurrent invocation.
func scatter(ctx context.Context, parent *http.Request, targets pool.Targets, cfg Config, perSlot func(idx int, r *Result)) ([]Result, error) {
	results := make([]Result, len(targets))
	return scatterInto(ctx, parent, targets, cfg, perSlot, results)
}

func scatterInto(ctx context.Context, parent *http.Request, targets pool.Targets, cfg Config, perSlot func(idx int, r *Result), results []Result) ([]Result, error) {
	metrics.ALBFanoutAttempts.WithLabelValues(cfg.Mechanism, cfg.Variant).Inc()
	l := len(targets)
	if l == 0 {
		return results, nil
	}

	var eg errgroup.Group
	if cfg.ConcurrencyLimit > 0 {
		eg.SetLimit(cfg.ConcurrencyLimit)
	}

	// Aggregate capture-buffer budget across all slots. Each dispatched slot
	// debits the worst-case per-slot reservation (cfg.MaxCaptureBytes). When
	// the budget would go negative, the slot is fail-fasted before
	// PrepareClone (and therefore before the capture buffer is allocated)
	// so the merge sees it as a failure and the existing partial-merge /
	// 502 fallback handles it.
	var budget atomic.Int64
	aggregateCap := cfg.MaxFanoutCaptureBytes > 0
	if aggregateCap {
		budget.Store(int64(cfg.MaxFanoutCaptureBytes))
	}
	perSlotReserve := perSlotReserveBytes(cfg)

	for i := range l {
		if targets[i] == nil {
			results[i] = Result{Index: i, Failed: true}
			continue
		}
		if aggregateCap && budget.Add(-perSlotReserve) < 0 {
			results[i] = Result{Index: i, Failed: true}
			metrics.ALBFanoutFailures.WithLabelValues(cfg.Mechanism, cfg.Variant, "aggregate_cap").Inc()
			continue
		}
		eg.Go(func() error {
			results[i].Index = i
			defer mech.RecoverFanoutPanic(cfg.Mechanism, cfg.Variant, i, func() {
				results[i].Failed = true
				results[i].Capture = nil
			})

			r2, crw, err := PrepareClone(ctx, parent, i, cfg)
			if err != nil {
				results[i].Failed = true
				results[i].Err = err
				metrics.ALBFanoutFailures.WithLabelValues(cfg.Mechanism, cfg.Variant, "clone").Inc()
				return err
			}
			results[i].Request = r2
			results[i].Capture = crw

			targets[i].Handler().ServeHTTP(crw, r2)
			// short_read wins over truncated; both can be true for the same
			// slot and double-counting distorts dashboards.
			if capt := request.GetUpstreamShortReadCapture(r2.Context()); capt != nil && capt.Tripped() {
				results[i].Failed = true
				reason := failureReason(targets[i], "short_read")
				metrics.ALBFanoutFailures.WithLabelValues(cfg.Mechanism, cfg.Variant, reason).Inc()
			} else if crw.Truncated() {
				results[i].Failed = true
				reason := failureReason(targets[i], "truncated")
				metrics.ALBFanoutFailures.WithLabelValues(cfg.Mechanism, cfg.Variant, reason).Inc()
			}
			if perSlot != nil {
				perSlot(i, &results[i])
			}
			if cfg.OnResult != nil {
				cfg.OnResult(i, &results[i])
			}
			return nil
		})
	}

	err := eg.Wait()
	if err != nil {
		logger.Warn("alb fanout gather failure", logging.Pairs{
			"mech": cfg.Mechanism, "error": err,
		})
	}
	return results, err
}

// PrimeBody ensures parent has a Resources value and a cached body so
// fanout goroutines can clone it concurrently without racing on r.Body.
// Returns the (possibly new) request; callers must use the returned value
// as their parent for the subsequent fanout call. No-op for GET/HEAD.
func PrimeBody(parent *http.Request) (*http.Request, error) {
	if request.GetResources(parent) == nil {
		parent = request.SetResources(parent, &request.Resources{})
	}
	if _, err := request.GetBody(parent); err != nil {
		return parent, err
	}
	return parent, nil
}

// PrepareClone produces one safe, capture-wrapped clone of parent suitable
// for handing to a pool member's handler. Mechanisms that own their own
// goroutine orchestration (FR) call this to avoid re-implementing the
// clone + ctx + resources + bounded capture sequence.
//
// PrepareClone does NOT prime parent's body; callers using their own
// goroutine pool must call request.GetBody on parent before spawning, or
// the per-clone GetBody calls will race on r.Body / rsc.RequestBody.
func PrepareClone(ctx context.Context, parent *http.Request, idx int, cfg Config) (*http.Request, *capture.CaptureResponseWriter, error) {
	r2, err := request.CloneWithoutResources(parent)
	if err != nil {
		return nil, nil, err
	}
	cloneCtx := ctx
	if cfg.Context != nil {
		cloneCtx = cfg.Context(ctx)
	}
	cloneCtx, _ = request.WithUpstreamShortReadCapture(cloneCtx)
	r2 = r2.WithContext(cloneCtx)
	if cfg.Resources != nil {
		if rsc := cfg.Resources(idx); rsc != nil {
			r2 = request.SetResources(r2, rsc)
		}
	}
	maxBytes := cfg.MaxCaptureBytes
	if maxBytes == 0 {
		maxBytes = capture.DefaultMaxBytes
	}
	crw := capture.NewCaptureResponseWriterWithLimit(maxBytes)
	return r2, crw, nil
}
