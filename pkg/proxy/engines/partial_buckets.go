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

package engines

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const spanFetchPartialBucket = "FetchPartialBucket"

var (
	// ErrPartialBucketFetch is returned when the origin answers a partial bucket's request with an
	// error or an empty body
	ErrPartialBucketFetch = errors.New("partial bucket fetch failed")
	// ErrPartialBucketModel is returned when a partial bucket's response can't be modeled as a dataset
	ErrPartialBucketModel = errors.New("partial bucket response is not a dataset")
)

// FetchPartialBucket sends a provider's partial bucket request on path pc, or r's own when nil,
// through the object proxy cache for partial_bucket_ttl and returns its rows
func FetchPartialBucket(r *http.Request, pc *po.Options, trq *timeseries.TimeRangeQuery,
	modeler *timeseries.Modeler,
) (timeseries.Timeseries, status.LookupStatus, error) {
	rsc := request.GetResources(r)
	if rsc == nil || rsc.BackendOptions == nil || modeler == nil {
		return nil, status.LookupStatusError, ErrPartialBucketFetch
	}
	if pc == nil {
		pc = rsc.PathConfig
	}
	o := rsc.BackendOptions
	qp, body, isBody := params.GetRequestValues(r)
	rs := request.NewResources(o, pc, rsc.CacheConfig, rsc.CacheClient, rsc.BackendClient, rsc.Tracer)
	// every bucket range has its own entry, whatever parameters the path keys
	rs.TimeRangeQuery = &timeseries.TimeRangeQuery{
		CacheKeyElements: unalignedKeyElements(qp, body, isBody, pc),
	}
	rs.AlternateCacheTTL, rs.PerCredentialCache = time.Duration(o.PartialBucketTTL), true
	ctx := profile.ToContext(r.Context(), dpcUpstreamEncodingProfile(rsc.TSReqestOptions))
	_, span := tspan.NewChildSpan(ctx, rsc.Tracer, spanFetchPartialBucket)
	if span != nil {
		ctx = trace.ContextWithSpan(ctx, span)
		defer span.End()
	}
	setResourceSpanAttributes(rs, span)
	r = r.WithContext(tctx.WithResources(ctx, rs))
	b, resp, isHit := FetchViaObjectProxyCache(r)
	if resp == nil || resp.StatusCode != http.StatusOK || len(b) == 0 {
		return nil, status.LookupStatusProxyError, ErrPartialBucketFetch
	}
	ts, err := modeler.WireUnmarshalerReader(getTimeseriesReader(resp), trq)
	if err != nil {
		logger.Error("partial bucket unmarshaling failed", logging.Pairs{keys.Detail: err.Error()})
		return nil, status.LookupStatusProxyError, err
	}
	// the provider's model, which may carry more than its rows, as long as it holds a DataSet
	if _, ok := ts.(dataset.Based); !ok {
		return nil, status.LookupStatusProxyError, ErrPartialBucketModel
	}
	if isHit {
		return ts, status.LookupStatusHit, nil
	}
	return ts, status.LookupStatusKeyMiss, nil
}

type partialFetch struct {
	pb     timeseries.PartialBucket
	ts     timeseries.Timeseries
	status status.LookupStatus
	err    error
}

type partialFetches struct {
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	limiter fetchLimiter
	fetched [2]partialFetch
	count   int
}

type fetchLimiter chan struct{}

func (l fetchLimiter) acquire(done <-chan struct{}) bool {
	// a nil limiter never waits, and a nil done waits for a slot however long it takes
	if l == nil {
		return true
	}
	select {
	case l <- struct{}{}:
		return true
	case <-done:
		return false
	}
}

func (l fetchLimiter) release() {
	if l != nil {
		<-l
	}
}

func startPartialBuckets(r *http.Request, o *bo.Options, client backends.TimeseriesBackend,
	trq *timeseries.TimeRangeQuery, now time.Time,
) *partialFetches {
	// each caller fetches its own partial buckets alongside the interior, since callers that share an
	// interior usually differ in their raw edges
	if trq.PartialCount == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(r.Context())
	pf := &partialFetches{
		cancel: cancel, count: int(trq.PartialCount),
		limiter: make(fetchLimiter, fetchConcurrencyLimit(o)),
	}
	for i := range pf.count {
		f := &pf.fetched[i]
		f.pb = trq.Partials[i]
		// each fetch gets its own request and resources, so none races the interior's
		rq, err := request.Clone(r)
		if err != nil {
			f.status, f.err = status.LookupStatusError, err
			continue
		}
		live := f.pb.IsLive(trq.Step, trq.Phase, now)
		pf.wg.Go(func() {
			defer func() {
				if rec := recover(); rec != nil {
					f.ts, f.status, f.err = nil, status.LookupStatusError, ErrPartialBucketFetch
					logger.Error("partial bucket fetch panicked", logging.Pairs{keys.Detail: rec})
				}
			}()
			if !pf.limiter.acquire(ctx.Done()) {
				f.status, f.err = status.LookupStatusError, ctx.Err()
				return
			}
			defer pf.limiter.release()
			f.ts, f.status, f.err = client.FetchPartialBucket(rq.WithContext(ctx), trq, f.pb, live)
		})
	}
	return pf
}

func (pf *partialFetches) sharedLimiter() fetchLimiter {
	// the origin calls of the request's interior count against the same limit as its partial buckets
	if pf == nil {
		return nil
	}
	return pf.limiter
}

func (pf *partialFetches) stop() {
	// abandons the fetches of a request that won't be answered with them
	if pf == nil {
		return
	}
	pf.cancel()
	pf.wg.Wait()
}

func (pf *partialFetches) mergeInto(rts timeseries.Timeseries, o *bo.Options, now time.Time,
	tr *tracing.Tracer, span trace.Span,
) ([]headers.PartialBucketResult, int64) {
	// merges each partial bucket's rows at its label into rts, which must be the caller's own, and
	// returns a report for each bucket and the values it added
	if pf == nil {
		return nil, 0
	}
	pf.wg.Wait()
	pf.cancel()
	results := make([]headers.PartialBucketResult, pf.count)
	merged := make([]timeseries.Timeseries, 0, pf.count)
	var values int64
	var beforeInterior bool
	for i := range pf.count {
		f := &pf.fetched[i]
		upper := f.pb.Upper
		if upper.IsZero() {
			upper = now
		}
		res := &results[i]
		res.Extent = timeseries.Extent{Start: f.pb.Lower, End: upper}
		res.Edge = f.pb.Edge
		switch {
		case f.err != nil || f.ts == nil:
			// the bucket is left out of the response, as a failed Fast Forward is
			res.Status = statusErr
		default:
			res.Status = f.status.String()
			keepLabel(f.ts, f.pb.Label)
			values += f.ts.ValueCount()
			merged = append(merged, f.ts)
			beforeInterior = beforeInterior || f.pb.Edge == timeseries.BucketEdgeStart
		}
		if o != nil {
			metrics.ProxyPartialBucketFetches.WithLabelValues(o.Name, o.Provider, res.Edge.String(),
				res.Status).Inc()
		}
	}
	if len(merged) > 0 {
		// a start bucket's label precedes the interior's, so the points are sorted after it joins
		rts.Merge(beforeInterior, merged...)
	}
	tspan.SetAttributes(tr, span, attribute.String(keys.PartialBuckets, headers.PartialBucketsString(results)))
	return results, values
}

func keepLabel(ts timeseries.Timeseries, label time.Time) {
	// the rows answered for the bucket's own label, and never a neighbor the origin also returned
	b, ok := ts.(dataset.Based)
	if !ok {
		return
	}
	ds := b.Base()
	e := epoch.Epoch(label.UnixNano())
	for _, res := range ds.Results {
		if res == nil {
			continue
		}
		for _, s := range res.SeriesList {
			if s == nil {
				continue
			}
			n := 0
			for _, p := range s.Points {
				if p.Epoch == e {
					s.Points[n] = p
					n++
				}
			}
			s.Points = s.Points[:n]
			s.PointSize = s.Points.Size()
		}
	}
	ds.ExtentList = timeseries.ExtentList{{Start: label, End: label}}
}
