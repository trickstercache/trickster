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

package nativedelta

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const partialStatusErr = "err" // a partial bucket left out of its response, as the HTTP engines report it

var (
	errPartialUnsupported = errors.New("the listener cannot fetch partial buckets")
	errPartialPanic       = errors.New("partial bucket fetch panicked")
	errPartialColumns     = errors.New("partial bucket columns differ from the response's")
)

// RequestStepAlignment returns the mode plan's statement names in a directive, else configured
func RequestStepAlignment(configured timeseries.StepAlignment, plan *sqlanalyzer.QueryPlan) timeseries.StepAlignment {
	if plan != nil && plan.Directives.StepAlignment != 0 {
		return plan.Directives.StepAlignment
	}
	return configured
}

func (e *Engine[R]) stepAlignment(requested timeseries.StepAlignment) timeseries.StepAlignment {
	// off never reaches the engine: each listener answers it from its object tier
	if requested == 0 {
		return sqlanalyzer.DefaultStepAlignment
	}
	if requested == timeseries.StepAlignmentOff || !requested.IsMode() ||
		sqlanalyzer.StepAlignments&requested == 0 {
		metrics.StepAlignmentFallbacks.WithLabelValues(e.cfg.BackendName, requested.String(),
			sqlanalyzer.DefaultStepAlignment.String()).Inc()
		return sqlanalyzer.DefaultStepAlignment
	}
	return requested
}

func (e *Engine[R]) partialBucketTTL() time.Duration {
	if e.cfg.PartialBucketTTL > 0 {
		return e.cfg.PartialBucketTTL
	}
	return bo.DefaultPartialBucketTTL
}

func (e *Engine[R]) executeUnaligned(req DeltaRequest[R]) (Outcome[R], cachestatus.LookupStatus, error) {
	// every bucket of a range holding no complete bucket is partial, so the answer is the origin's own
	// to the client's statement, kept only as long as a partial bucket
	if req.Ops.FetchPartial == nil || req.Statement == "" {
		return e.original(req)
	}
	payload, lookup, err := req.Ops.FetchPartial(requestContext(req), req.Statement, e.partialBucketTTL())
	return Outcome[R]{Object: payload}, lookup, err
}

type partialFetch struct {
	pb     timeseries.PartialBucket
	rows   *Delta
	status cachestatus.LookupStatus
	err    error
}

type partialFetches struct {
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	fetched [2]partialFetch
	count   int
}

func requestContext[R any](req DeltaRequest[R]) context.Context {
	if req.Context != nil {
		return req.Context
	}
	return context.Background()
}

func (e *Engine[R]) fetchPartials(req DeltaRequest[R], window *Window, concurrent bool) *partialFetches {
	// startPartials' goroutines move req to the heap on entry, which a window without partials never pays
	if window.PartialCount == 0 {
		return nil
	}
	return e.startPartials(req, window, concurrent)
}

func (e *Engine[R]) startPartials(req DeltaRequest[R], window *Window, concurrent bool) *partialFetches {
	ctx, cancel := context.WithCancel(requestContext(req))
	pf := &partialFetches{count: int(window.PartialCount), cancel: cancel}
	for i := range pf.count {
		f := &pf.fetched[i]
		f.pb = window.Partials[i]
		if !concurrent {
			e.fetchPartial(ctx, req, f)
			continue
		}
		pf.wg.Go(func() {
			defer func() {
				if rec := recover(); rec != nil {
					f.rows, f.err = nil, errPartialPanic
					logger.Error("partial bucket fetch panicked", logging.Pairs{keys.Detail: rec})
				}
			}()
			e.fetchPartial(ctx, req, f)
		})
	}
	return pf
}

func (pf *partialFetches) wait() {
	if pf != nil {
		pf.wg.Wait()
		pf.cancel()
	}
}

func (pf *partialFetches) abandon() {
	// cancels fetches whose rows no response will hold; each writes only its own result, which nothing
	// reads, so none is waited for
	if pf != nil {
		pf.cancel()
	}
}

func (e *Engine[R]) fetchPartial(ctx context.Context, req DeltaRequest[R], f *partialFetch) {
	// the bucket's rows over the client's own range, from the object tier; only its label is kept, so a
	// neighbor the origin also answered never reaches the response
	if req.Ops.FetchPartial == nil || req.Ops.Model == nil {
		f.err = errPartialUnsupported
		return
	}
	statement, err := req.Plan.RenderRange(f.pb)
	if err != nil {
		e.observeRewriteFailure("render_range")
		f.err = err
		return
	}
	result, lookup, err := req.Ops.FetchPartial(ctx, statement, e.partialBucketTTL())
	if err != nil {
		f.err = err
		return
	}
	rows, err := req.Ops.Model(result)
	if err != nil {
		f.err = err
		return
	}
	if rows == nil || rows.DS == nil {
		f.err = errPartialUnsupported
		return
	}
	f.rows = &Delta{Header: rows.Header, DS: rows.DS.View(timeseries.Extent{Start: f.pb.Label, End: f.pb.Label})}
	f.status = lookup
}

func (e *Engine[R]) withPartials(req DeltaRequest[R], interior *Delta, pf *partialFetches) *Delta {
	// the interior's rows with each fetched partial bucket at its label, which never meets an interior
	// label; a bucket that failed is left out and reported, as the HTTP engines do
	if pf == nil {
		return interior
	}
	pf.wait()
	same := req.Ops.SameHeader
	if same == nil {
		same = bytes.Equal
	}
	var start, end *dataset.DataSet
	provider := e.cfg.Provider
	if provider == "" {
		provider = e.cfg.Protocol
	}
	for i := range pf.count {
		f := &pf.fetched[i]
		if f.err == nil && !same(interior.Header, f.rows.Header) {
			f.err = errPartialColumns
		}
		result := f.status.String()
		switch {
		case f.err != nil:
			result = partialStatusErr
			logger.Debug("partial bucket left out of the response", logging.Pairs{
				keys.Protocol: e.cfg.Protocol, keys.BackendName: e.cfg.BackendName,
				keys.Detail: f.err.Error(),
			})
		case f.pb.Edge == timeseries.BucketEdgeStart:
			start = f.rows.DS
		default:
			end = f.rows.DS
		}
		metrics.ProxyPartialBucketFetches.WithLabelValues(e.cfg.BackendName, provider,
			f.pb.Edge.String(), result).Inc()
	}
	if start == nil && end == nil {
		return interior
	}
	// in time order, so no series needs sorting
	// the interior's points stay in place, with the buckets beside them rather than copied in with them
	return &Delta{Header: interior.Header, DS: dataset.MergeDisjointParts(req.Plan.Step, start, interior.DS, end)}
}
