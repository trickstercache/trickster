/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tricksterproxy/trickster/pkg/backends"
	tc "github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/evictionmethods"
	"github.com/tricksterproxy/trickster/pkg/cache/status"
	"github.com/tricksterproxy/trickster/pkg/locks"
	tl "github.com/tricksterproxy/trickster/pkg/observability/logging"
	"github.com/tricksterproxy/trickster/pkg/observability/metrics"
	tspan "github.com/tricksterproxy/trickster/pkg/observability/tracing/span"
	tctx "github.com/tricksterproxy/trickster/pkg/proxy/context"
	tpe "github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	"github.com/tricksterproxy/trickster/pkg/timeseries"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// DeltaProxyCache is used for Time Series Acceleration, but not for normal HTTP Object Caching

// DeltaProxyCacheRequest identifies the gaps between the cache and a new timeseries request,
// requests the gaps from the origin server and returns the reconstituted dataset to the downstream
// request while caching the results for subsequent requests of the same data
func DeltaProxyCacheRequest(w http.ResponseWriter, r *http.Request, modeler *timeseries.Modeler) {
	rsc := request.GetResources(r)
	if modeler != nil {
		rsc.TSMarshaler = modeler.WireMarshalWriter
		rsc.TSUnmarshaler = modeler.WireUnmarshaler
	}
	o := rsc.BackendOptions
	ctx, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "DeltaProxyCacheRequest")
	if span != nil {
		defer span.End()
	}
	r = r.WithContext(ctx)

	pc := rsc.PathConfig
	cache := rsc.CacheClient
	cc := rsc.CacheConfig
	locker := cache.Locker()

	client := rsc.BackendClient.(backends.TimeseriesBackend)

	trq, rlo, canOPC, err := client.ParseTimeRangeQuery(r)
	rsc.TimeRangeQuery = trq
	rsc.TSReqestOptions = rlo
	if err != nil {
		if canOPC {
			tl.Debug(rsc.Logger, "could not parse time range query, using object proxy cache", tl.Pairs{"error": err.Error()})
			rsc.AlternateCacheTTL = time.Second * o.FastForwardTTL
			ObjectProxyCacheRequest(w, r)
			return
		}
		// err may simply mean incompatible query (e.g., non-select), so just proxy
		DoProxy(w, r, true)
		return
	}

	var cacheStatus status.LookupStatus

	pr := newProxyRequest(r, w)
	rlo.FastForwardDisable = o.FastForwardDisable || rlo.FastForwardDisable
	trq.NormalizeExtent()
	now := time.Now()

	OldestRetainedTimestamp := time.Time{}
	if o.TimeseriesEvictionMethod == evictionmethods.EvictionMethodOldest {
		OldestRetainedTimestamp = now.Truncate(trq.Step).Add(-(trq.Step * o.TimeseriesRetention))
		if trq.Extent.End.Before(OldestRetainedTimestamp) {
			tl.Debug(pr.Logger, "timerange end is too early to consider caching",
				tl.Pairs{"oldestRetainedTimestamp": OldestRetainedTimestamp,
					"step": trq.Step, "retention": o.TimeseriesRetention})
			DoProxy(w, r, true)
			return
		}
	}

	client.SetExtent(pr.upstreamRequest, trq, &trq.Extent)
	key := o.CacheKeyPrefix + ".dpc." + pr.DeriveCacheKey(trq.TemplateURL, "")
	pr.cacheLock, _ = locker.RAcquire(key)

	// this is used to determine if Fast Forward should be activated for this request
	normalizedNow := &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: now},
		Step:   trq.Step,
	}
	normalizedNow.NormalizeExtent()

	var cts timeseries.Timeseries
	var doc *HTTPDocument
	var elapsed time.Duration

	coReq := GetRequestCachingPolicy(r.Header)
checkCache:
	if coReq.NoCache {
		if span != nil {
			span.AddEvent("Not Caching")
		}
		cacheStatus = status.LookupStatusPurge
		go cache.Remove(key)
		cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
		if err != nil {
			pr.cacheLock.RRelease()
			h := doc.SafeHeaderClone()
			recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
				r.URL.Path, "", elapsed.Seconds(), nil, h)
			Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
			return // fetchTimeseries logs the error
		}
	} else {
		doc, cacheStatus, _, err = QueryCache(ctx, cache, key, nil)
		if cacheStatus == status.LookupStatusKeyMiss && err == tc.ErrKNF {
			cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
			if err != nil {
				pr.cacheLock.RRelease()
				h := doc.SafeHeaderClone()
				recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
					r.URL.Path, "", elapsed.Seconds(), nil, h)
				Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
				return // fetchTimeseries logs the error
			}
		} else {
			// Load the Cached Timeseries
			if doc == nil {
				err = tpe.ErrEmptyDocumentBody
			} else {
				if cc.Provider == "memory" {
					cts = doc.timeseries
				} else {
					cts, err = modeler.CacheUnmarshaler(doc.Body, trq)
				}
			}
			if err != nil {
				tl.Error(pr.Logger, "cache object unmarshaling failed",
					tl.Pairs{"key": key, "backendName": client.Name(), "detail": err.Error()})
				go cache.Remove(key)
				cts, doc, elapsed, err = fetchTimeseries(pr, trq, client, modeler)
				if err != nil {
					pr.cacheLock.RRelease()
					h := doc.SafeHeaderClone()
					recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
						r.URL.Path, "", elapsed.Seconds(), nil, h)
					Respond(w, doc.StatusCode, h, bytes.NewReader(doc.Body))
					return // fetchTimeseries logs the error
				}
			} else {
				if o.TimeseriesEvictionMethod == evictionmethods.EvictionMethodLRU {
					el := cts.Extents()
					tsc := cts.TimestampCount()
					if tsc > 0 &&
						tsc >= int64(o.TimeseriesRetentionFactor) {
						if trq.Extent.End.Before(el[0].Start) {
							pr.cacheLock.RRelease()
							tl.Debug(pr.Logger, "timerange end is too early to consider caching",
								tl.Pairs{"step": trq.Step, "retention": o.TimeseriesRetention})
							DoProxy(w, r, true)
							return
						}
					}
				}
				cacheStatus = status.LookupStatusPartialHit
			}
		}
	}

	// Find the ranges that we want, but which are not currently cached
	var missRanges timeseries.ExtentList
	if cacheStatus == status.LookupStatusPartialHit {
		missRanges = cts.Extents().CalculateDeltas(trq.Extent, trq.Step)
	}

	if len(missRanges) == 0 && cacheStatus == status.LookupStatusPartialHit {
		// on full cache hit, elapsed records the time taken to query the cache
		// and definitively conclude that it is a full cache hit
		elapsed = time.Since(now)
		cacheStatus = status.LookupStatusHit
	} else if len(missRanges) == 1 && missRanges[0].Start.Equal(trq.Extent.Start) &&
		missRanges[0].End.Equal(trq.Extent.End) {
		cacheStatus = status.LookupStatusRangeMiss
	}

	tspan.SetAttributes(rsc.Tracer, span, attribute.String("cache.status", cacheStatus.String()))

	var writeLock locks.NamedLock

	if cacheStatus == status.LookupStatusHit {
		// In a cache hit, nothing changes so we just release the reader lock
		pr.cacheLock.RRelease()
	} else {
		// in this case, it's not a cache hit, so something is _likely_ going to be cached now.
		// we write lock here, so as to prevent other concurrent client requests for the same url,
		// which will have the same cacheStatus, from causing the same or similar HTTP requests
		// to be made against the origin, since just one should do.

		// acquire a write lock via the Upgrade method, which will swap the read lock for a
		// write lock, and return true if this client was the only one, or otherwise the first
		// client in a concurrent read lock group to request an Upgrade.
		wasFirst := pr.cacheLock.Upgrade()

		// if this request was first, it is good to proceed with upstream communications and caching.
		// when another requests was first to acquire the mutex, we will jump up to checkCache
		// to get the refreshed version. after 3 reiterations, we'll proceed anyway to avoid long loops.
		if !wasFirst && pr.rerunCount < 3 {
			// we weren't first, so quickly drop our write lock, and re-run the request
			pr.cacheLock.Release()
			pr.cacheLock, _ = locker.RAcquire(key)
			pr.rerunCount++
			goto checkCache
		}
		writeLock = pr.cacheLock
	}

	ffStatus := "off"
	var ffReq *http.Request
	// if the step resolution <= Fast Forward TTL, then no need to even try Fast Forward
	if !rlo.FastForwardDisable {
		if trq.Step > o.FastForwardTTL {
			ffReq, err = client.FastForwardRequest(r)
			if err != nil || ffReq == nil || ffReq.URL == nil || ffReq.URL.Scheme == "" {
				ffStatus = "err"
				rlo.FastForwardDisable = true
			} else {
				rs := request.NewResources(o, o.FastForwardPath, cc, cache, client, rsc.Tracer, pr.Logger)
				rs.AlternateCacheTTL = o.FastForwardTTL
				ffReq = ffReq.WithContext(tctx.WithResources(ffReq.Context(), rs))
			}
		} else {
			rlo.FastForwardDisable = true
		}
	}

	dpStatus := tl.Pairs{
		"cacheKey":    key,
		"cacheStatus": cacheStatus,
		"reqStart":    trq.Extent.Start.Unix(),
		"reqEnd":      trq.Extent.End.Unix(),
	}
	if len(missRanges) > 0 {
		dpStatus["extentsFetched"] = missRanges.String()
	}

	// maintain a list of timeseries to merge into the main timeseries
	mts := make([]timeseries.Timeseries, 0, len(missRanges))
	wg := sync.WaitGroup{}
	appendLock := sync.Mutex{}
	var uncachedValueCount int64

	// determine backfill tolerance start window
	bt := o.BackfillTolerance              // todo, deterministic when we add in points vs time option
	bfs := now.Add(-bt).Truncate(trq.Step) // start of the backfill tolerance window

	// iterate each time range that the client needs and fetch from the upstream origin
	for i := range missRanges {

		// this implements backfill tolerance by expanding the miss range to
		// include any cached-but-volatile ranges
		if missRanges[i].End.After(bfs) {
			if trq.Extent.Start.Before(bfs) {
				if bfs.Before(missRanges[i].Start) {
					missRanges[i].Start = bfs
				}
			} else {
				missRanges[i].Start = trq.Extent.Start
			}
		}

		wg.Add(1)
		// This fetches the gaps from the origin and adds their datasets to the merge list
		go func(e *timeseries.Extent, rq *proxyRequest) {
			defer wg.Done()
			rq.upstreamRequest = rq.WithContext(tctx.WithResources(
				trace.ContextWithSpan(context.Background(), span),
				request.NewResources(o, pc, cc, cache, client, rsc.Tracer, pr.Logger)))
			client.SetExtent(rq.upstreamRequest, trq, e)

			ctxMR, spanMR := tspan.NewChildSpan(rq.upstreamRequest.Context(), rsc.Tracer, "FetchRange")
			if spanMR != nil {
				rq.upstreamRequest = rq.upstreamRequest.WithContext(ctxMR)
				defer spanMR.End()
			}

			body, resp, _ := rq.Fetch()
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, err := modeler.WireUnmarshaler(body, trq)
				if err != nil {
					tl.Error(pr.Logger, "proxy object unmarshaling failed",
						tl.Pairs{"body": string(body)})
					return
				}
				doc.headerLock.Lock()
				headers.Merge(doc.Headers, resp.Header)
				doc.headerLock.Unlock()
				uncachedValueCount += nts.ValueCount()
				nts.SetTimeRangeQuery(trq)
				nts.SetExtents([]timeseries.Extent{*e})
				appendLock.Lock()
				mts = append(mts, nts)
				appendLock.Unlock()
			}
		}(&missRanges[i], pr.Clone())
	}

	var hasFastForwardData bool
	var ffts timeseries.Timeseries

	// Only fast forward if configured and the user request is for the absolute latest datapoint
	if (!rlo.FastForwardDisable) &&
		(trq.Extent.End.Equal(normalizedNow.Extent.End)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, span := tspan.NewChildSpan(ctx, rsc.Tracer, "FetchFastForward")
			if span != nil {
				ffReq = ffReq.WithContext(trace.ContextWithSpan(ffReq.Context(), span))
				defer span.End()
			}
			body, resp, isHit := FetchViaObjectProxyCache(ffReq)
			if resp != nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
				ffts, err = modeler.WireUnmarshaler(body, trq)
				if err != nil {
					ffStatus = "err"
					tl.Error(pr.Logger, "proxy object unmarshaling failed",
						tl.Pairs{"body": string(body)})
					return
				}
				ffts.SetTimeRangeQuery(trq)
				x := ffts.Extents()
				if isHit {
					ffStatus = "hit"
				} else {
					ffStatus = "miss"
				}
				hasFastForwardData = len(x) > 0 && x[0].End.After(trq.Extent.End)
			} else {
				ffStatus = "err"
			}
		}()
	}

	wg.Wait()

	// Merge the new delta timeseries into the cached timeseries
	if len(mts) > 0 {
		// on phit, elapsed records the time spent waiting for all upstream requests to complete
		elapsed = time.Since(now)
		cts.Merge(true, mts...)
	}

	// cts is the cacheable time series, rts is the user's response timeseries
	var rts timeseries.Timeseries
	if cacheStatus != status.LookupStatusKeyMiss {
		rts = cts.CroppedClone(trq.Extent)
	} else {
		rts = cts.Clone()
	}

	if writeLock != nil {
		// if the mutex is still locked, it means we need to write the time series to cache
		go func() {
			defer writeLock.Release()
			// Crop the Cache Object down to the Sample Size or Age Retention Policy and the
			// Backfill Tolerance before storing to cache
			switch o.TimeseriesEvictionMethod {
			case evictionmethods.EvictionMethodLRU:
				cts.CropToSize(o.TimeseriesRetentionFactor, now, trq.Extent)
			default:
				cts.CropToRange(timeseries.Extent{End: now, Start: OldestRetainedTimestamp})
			}
			// Don't cache datasets with empty extents
			// (everything was cropped so there is nothing to cache)
			if len(cts.Extents()) > 0 {
				if cc.Provider == "memory" {
					doc.timeseries = cts
				} else {
					cdata, err := modeler.CacheMarshaler(cts, nil, 0)
					if err != nil {
						tl.Error(pr.Logger, "error marshaling timeseries", tl.Pairs{
							"cacheKey": key,
							"detail":   err.Error(),
						})
						return
					}
					doc.Body = cdata
				}
				if err := WriteCache(ctx, cache, key, doc, o.TimeseriesTTL, o.CompressableTypes); err != nil {
					tl.Error(pr.Logger, "error writing object to cache",
						tl.Pairs{
							"backendName": o.Name,
							"cacheName":   cache.Configuration().Name,
							"cacheKey":    key,
							"detail":      err.Error(),
						},
					)
				}
			}
		}()
	}

	cachedValueCount := rts.ValueCount() - uncachedValueCount

	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(o.Name,
			o.Provider, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}

	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(o.Name,
			o.Provider, "cached", r.URL.Path).Add(float64(cachedValueCount))
	}

	// Merge Fast Forward data if present. This must be done after the Downstream Crop since
	// the cropped extent was normalized to stepboundaries and would remove fast forward data
	// If the fast forward data point is older (e.g. cached) than the last datapoint in the
	// returned time series, it will not be merged
	if hasFastForwardData && len(ffts.Extents()) == 1 &&
		ffts.Extents()[0].Start.Truncate(time.Second).After(normalizedNow.Extent.End) {
		rts.Merge(false, ffts)
	}
	rts.SetExtents(nil) // so they are not included in the client response json
	//rts.SetTimeRangeQuery(&timeseries.TimeRangeQuery{})
	rh := doc.SafeHeaderClone()
	sc := doc.StatusCode

	// Respond to the user. Using the response headers from a Delta Response,
	// so as to not map conflict with cacheData on WriteCache
	logDeltaRoutine(pr.Logger, dpStatus)
	recordDPCResult(r, cacheStatus, sc, r.URL.Path, ffStatus, elapsed.Seconds(), missRanges, rh)

	rsc.TS = rts
	Respond(w, 0, rh, nil) // body and code are nil so this only sets appropriate headers; no writes
	if rsc.IsMergeMember { // don't bother marshaling this dataset if it's just going to be merged internally
		if rsc.Response == nil {
			rsc.Response = &http.Response{StatusCode: sc}
		}
		return
	}
	modeler.WireMarshalWriter(rts, rlo, sc, w)
}

func logDeltaRoutine(logger interface{}, p tl.Pairs) {
	tl.Debug(logger, "delta routine completed", p)
}

func fetchTimeseries(pr *proxyRequest, trq *timeseries.TimeRangeQuery,
	client backends.TimeseriesBackend, modeler *timeseries.Modeler) (timeseries.Timeseries,
	*HTTPDocument, time.Duration, error) {

	rsc := request.GetResources(pr.Request)
	o := rsc.BackendOptions
	pc := rsc.PathConfig

	var handlerName string
	if pc != nil {
		handlerName = pc.HandlerName
	}

	ctx, span := tspan.NewChildSpan(pr.upstreamRequest.Context(), rsc.Tracer, "FetchTimeSeries")
	if span != nil {
		defer span.End()
	}
	pr.upstreamRequest = pr.upstreamRequest.WithContext(ctx)

	start := time.Now()
	_, resp, _ := PrepareFetchReader(pr.upstreamRequest)

	pr.upstreamResponse = resp
	rsc.Response = resp

	d := &HTTPDocument{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
	}

	if resp.StatusCode != 200 {
		var b []byte
		if resp.Body != nil {
			b, _ := io.ReadAll(resp.Body)
			if len(b) > 128 {
				b = b[:128]
			}
		}
		tl.Error(pr.Logger, "unexpected upstream response",
			tl.Pairs{
				"statusCode":              resp.StatusCode,
				"clientRequestURL":        pr.Request.URL.String(),
				"clientRequestMethod":     pr.Request.Method,
				"clientRequestHeaders":    pr.Request.Header,
				"upstreamRequestURL":      pr.upstreamRequest.URL.String(),
				"upstreamRequestMethod":   pr.upstreamRequest.Method,
				"upstreamRequestHeaders":  headers.LogString(pr.upstreamRequest.Header),
				"upstreamResponseHeaders": headers.LogString(resp.Header),
				"upstreamResponseBody":    string(b),
			},
		)
		return nil, d, time.Duration(0), tpe.ErrUnexpectedUpstreamResponse
	}

	ts, err := modeler.WireUnmarshalerReader(resp.Body, trq)
	if err != nil {
		tl.Error(pr.Logger,
			"proxy object unmarshaling failed", tl.Pairs{"detail": err.Error()})
		return nil, d, time.Duration(0), err
	}

	elapsed := time.Since(start)
	go logUpstreamRequest(pr.Logger, o.Name, o.Provider, handlerName,
		pr.Method, pr.URL.String(), pr.UserAgent(), resp.StatusCode, 0, elapsed.Seconds())

	return ts, d, elapsed, nil
}

func recordDPCResult(r *http.Request, cacheStatus status.LookupStatus, httpStatus int, path,
	ffStatus string, elapsed float64, needed []timeseries.Extent, header http.Header) {
	recordResults(r, "DeltaProxyCache", cacheStatus, httpStatus, path, ffStatus, elapsed,
		timeseries.ExtentList(needed), header)
}
