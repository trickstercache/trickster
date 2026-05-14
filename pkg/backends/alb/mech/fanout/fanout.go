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
// All. Mechanisms that race for a first-good response (FR) own their own
// goroutine orchestration but use PrepareClone for the per-goroutine setup.
package fanout

import (
	"context"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"golang.org/x/sync/errgroup"
)

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
	l := len(targets)
	results := make([]Result, l)
	if l == 0 {
		return results, nil
	}

	var eg errgroup.Group
	if cfg.ConcurrencyLimit > 0 {
		eg.SetLimit(cfg.ConcurrencyLimit)
	}

	for i := range l {
		if targets[i] == nil {
			results[i] = Result{Index: i, Failed: true}
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
			if crw.Truncated() {
				results[i].Failed = true
				metrics.ALBFanoutFailures.WithLabelValues(cfg.Mechanism, cfg.Variant, "truncated").Inc()
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
