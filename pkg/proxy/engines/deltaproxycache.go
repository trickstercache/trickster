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
	"context"
	"net/http"
	"sync"
	"time"

	tc "github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/evictionmethods"
	"github.com/tricksterproxy/trickster/pkg/cache/status"
	"github.com/tricksterproxy/trickster/pkg/locks"
	tctx "github.com/tricksterproxy/trickster/pkg/proxy/context"
	tpe "github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/origins"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	tspan "github.com/tricksterproxy/trickster/pkg/tracing/span"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/metrics"

	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/trace"
)

// DeltaProxyCache is used for Time Series Acceleration, but not for normal HTTP Object Caching

// DeltaProxyCacheRequest identifies the gaps between the cache and a new timeseries request,
// requests the gaps from the origin server and returns the reconstituted dataset to the downstream
// request while caching the results for subsequent requests of the same data
func DeltaProxyCacheRequest(w http.ResponseWriter, r *http.Request) {

	rsc := request.GetResources(r)
	oc := rsc.OriginConfig

	ctx, span := tspan.NewChildSpan(r.Context(), rsc.Tracer, "DeltaProxyCacheRequest")
	if span != nil {
		defer span.End()
	}
	r = r.WithContext(ctx)

	pc := rsc.PathConfig
	cache := rsc.CacheClient
	cc := rsc.CacheConfig
	locker := cache.Locker()

	client := rsc.OriginClient.(origins.TimeseriesClient)

	trq, err := client.ParseTimeRangeQuery(r)
	if err != nil {
		// err may simply mean incompatible query (e.g., non-select), so just proxy
		DoProxy(w, r, true)
		return
	}

	var cacheStatus status.LookupStatus

	pr := newProxyRequest(r, w)
	trq.FastForwardDisable = oc.FastForwardDisable || trq.FastForwardDisable
	trq.NormalizeExtent()

	// this is used to ensure the head of the cache respects the BackFill Tolerance
	bf := timeseries.Extent{Start: time.Unix(0, 0), End: trq.Extent.End}
	bt := trq.GetBackfillTolerance(oc.BackfillTolerance)

	if !trq.IsOffset && bt > 0 && !time.Now().Add(-bt).After(bf.End) {
		bf.End = bf.End.Add(-bt)
	}

	now := time.Now()

	OldestRetainedTimestamp := time.Time{}
	if oc.TimeseriesEvictionMethod == evictionmethods.EvictionMethodOldest {
		OldestRetainedTimestamp = now.Truncate(trq.Step).Add(-(trq.Step * oc.TimeseriesRetention))
		if trq.Extent.End.Before(OldestRetainedTimestamp) {
			pr.Logger.Debug("timerange end is too early to consider caching",
				tl.Pairs{"oldestRetainedTimestamp": OldestRetainedTimestamp,
					"step": trq.Step, "retention": oc.TimeseriesRetention})
			DoProxy(w, r, true)
			return
		}
		if trq.Extent.Start.After(bf.End) {
			pr.Logger.Debug("timerange is too new to cache due to backfill tolerance",
				tl.Pairs{"backFillToleranceSecs": bt,
					"newestRetainedTimestamp": bf.End, "queryStart": trq.Extent.Start})
			DoProxy(w, r, true)
			return
		}
	}

	client.SetExtent(pr.upstreamRequest, trq, &trq.Extent)
	key := oc.CacheKeyPrefix + ".dpc." + pr.DeriveCacheKey(trq.TemplateURL, "")
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
			span.AddEvent(
				"Not Caching",
			)
		}
		cacheStatus = status.LookupStatusPurge
		go cache.Remove(key)
		cts, doc, elapsed, err = fetchTimeseries(pr, trq, client)
		if err != nil {
			pr.cacheLock.RRelease()
			h := doc.SafeHeaderClone()
			recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
				r.URL.Path, "", elapsed.Seconds(), nil, h)
			Respond(w, doc.StatusCode, h, doc.Body)
			return // fetchTimeseries logs the error
		}
	} else {
		doc, cacheStatus, _, err = QueryCache(ctx, cache, key, nil)
		if cacheStatus == status.LookupStatusKeyMiss && err == tc.ErrKNF {
			cts, doc, elapsed, err = fetchTimeseries(pr, trq, client)
			if err != nil {
				pr.cacheLock.RRelease()
				h := doc.SafeHeaderClone()
				recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
					r.URL.Path, "", elapsed.Seconds(), nil, h)
				Respond(w, doc.StatusCode, h, doc.Body)
				return // fetchTimeseries logs the error
			}
		} else {
			// Load the Cached Timeseries
			if doc == nil {
				err = tpe.ErrEmptyDocumentBody
			} else {
				if cc.CacheType == "memory" {
					cts = doc.timeseries
				} else {
					cts, err = client.UnmarshalTimeseries(doc.Body)
				}
			}
			if err != nil {
				pr.Logger.Error("cache object unmarshaling failed",
					tl.Pairs{"key": key, "originName": client.Name(), "detail": err.Error()})
				go cache.Remove(key)
				cts, doc, elapsed, err = fetchTimeseries(pr, trq, client)
				if err != nil {
					pr.cacheLock.RRelease()
					h := doc.SafeHeaderClone()
					recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode,
						r.URL.Path, "", elapsed.Seconds(), nil, h)
					Respond(w, doc.StatusCode, h, doc.Body)
					return // fetchTimeseries logs the error
				}
			} else {
				if oc.TimeseriesEvictionMethod == evictionmethods.EvictionMethodLRU {
					el := cts.Extents()
					tsc := cts.TimestampCount()
					if tsc > 0 &&
						tsc >= oc.TimeseriesRetentionFactor {
						if trq.Extent.End.Before(el[0].Start) {
							pr.cacheLock.RRelease()
							go pr.Logger.Debug("timerange end is too early to consider caching",
								tl.Pairs{"step": trq.Step, "retention": oc.TimeseriesRetention})
							DoProxy(w, r, true)
							return
						}
						if trq.Extent.Start.After(el[len(el)-1].End) {
							pr.cacheLock.RRelease()
							go pr.Logger.Debug("timerange not cached due to backfill tolerance",
								tl.Pairs{
									"backFillToleranceSecs":   bt,
									"newestRetainedTimestamp": bf.End,
									"queryStart":              trq.Extent.Start,
								},
							)
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
		missRanges = trq.CalculateDeltas(cts.Extents())
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

	tspan.SetAttributes(rsc.Tracer, span, label.String("cache.status", cacheStatus.String()))

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
	if !trq.FastForwardDisable {
		if trq.Step > oc.FastForwardTTL {
			ffReq, err = client.FastForwardRequest(r)
			if err != nil || ffReq == nil || ffReq.URL == nil || ffReq.URL.Scheme == "" {
				ffStatus = "err"
				trq.FastForwardDisable = true
			} else {
				rs := request.NewResources(oc, oc.FastForwardPath, cc, cache, client, rsc.Tracer, pr.Logger)
				rs.AlternateCacheTTL = oc.FastForwardTTL
				ffReq = ffReq.WithContext(tctx.WithResources(ffReq.Context(), rs))
			}
		} else {
			trq.FastForwardDisable = true
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
	uncachedValueCount := 0

	// iterate each time range that the client needs and fetch from the upstream origin
	for i := range missRanges {
		wg.Add(1)
		// This fetches the gaps from the origin and adds their datasets to the merge list
		go func(e *timeseries.Extent, rq *proxyRequest) {
			defer wg.Done()
			rq.upstreamRequest = rq.WithContext(tctx.WithResources(
				trace.ContextWithSpan(context.Background(), span),
				request.NewResources(oc, pc, cc, cache, client, rsc.Tracer, pr.Logger)))
			client.SetExtent(rq.upstreamRequest, trq, e)

			ctxMR, spanMR := tspan.NewChildSpan(rq.upstreamRequest.Context(), rsc.Tracer, "FetchRange")
			if spanMR != nil {
				rq.upstreamRequest = rq.upstreamRequest.WithContext(ctxMR)
				defer spanMR.End()
			}

			body, resp, _ := rq.Fetch()
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, err := client.UnmarshalTimeseries(body)
				if err != nil {
					pr.Logger.Error("proxy object unmarshaling failed",
						tl.Pairs{"body": string(body)})
					return
				}
				doc.headerLock.Lock()
				headers.Merge(doc.Headers, resp.Header)
				doc.headerLock.Unlock()
				uncachedValueCount += nts.ValueCount()
				nts.SetStep(trq.Step)
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
	if (!trq.FastForwardDisable) &&
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
				ffts, err = client.UnmarshalInstantaneous(body)
				if err != nil {
					ffStatus = "err"
					pr.Logger.Error("proxy object unmarshaling failed",
						tl.Pairs{"body": string(body)})
					return
				}
				ffts.SetStep(trq.Step)
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
	rts := cts.Clone()

	if writeLock != nil {
		// if the mutex is still locked, it means we need to write the time series to cache
		go func() {
			defer writeLock.Release()
			// Crop the Cache Object down to the Sample Size or Age Retention Policy and the
			// Backfill Tolerance before storing to cache
			switch oc.TimeseriesEvictionMethod {
			case evictionmethods.EvictionMethodLRU:
				cts.CropToSize(oc.TimeseriesRetentionFactor, bf.End, trq.Extent)
			default:
				cts.CropToRange(timeseries.Extent{End: bf.End, Start: OldestRetainedTimestamp})
			}
			// Don't cache datasets with empty extents
			// (everything was cropped so there is nothing to cache)
			if len(cts.Extents()) > 0 {
				if cc.CacheType == "memory" {
					doc.timeseries = cts
				} else {
					cdata, err := client.MarshalTimeseries(cts)
					if err != nil {
						pr.Logger.Error("error marshaling timeseries", tl.Pairs{
							"cacheKey": key,
							"detail":   err.Error(),
						})
						return
					}
					doc.Body = cdata
				}
				if err := WriteCache(ctx, cache, key, doc, oc.TimeseriesTTL, oc.CompressableTypes); err != nil {
					pr.Logger.Error("error writing object to cache",
						tl.Pairs{
							"originName": oc.Name,
							"cacheName":  cache.Configuration().Name,
							"cacheKey":   key,
							"detail":     err.Error(),
						},
					)
				}
			}
		}()
	}

	// if it was a cache key miss, there is no need to undergo Crop since the extents are identical
	if cacheStatus != status.LookupStatusKeyMiss {
		rts.CropToRange(trq.Extent)
	}
	cachedValueCount := rts.ValueCount() - uncachedValueCount

	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(oc.Name,
			oc.OriginType, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}

	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(oc.Name,
			oc.OriginType, "cached", r.URL.Path).Add(float64(cachedValueCount))
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
	rts.SetStep(0)
	rdata, err := client.MarshalTimeseries(rts)
	rh := doc.SafeHeaderClone()
	sc := doc.StatusCode

	// Respond to the user. Using the response headers from a Delta Response,
	// so as to not map conflict with cacheData on WriteCache
	logDeltaRoutine(pr.Logger, dpStatus)
	recordDPCResult(r, cacheStatus, sc, r.URL.Path, ffStatus, elapsed.Seconds(), missRanges, rh)
	Respond(w, sc, rh, rdata)
}

func logDeltaRoutine(log *tl.Logger, p tl.Pairs) { log.Debug("delta routine completed", p) }

func fetchTimeseries(pr *proxyRequest, trq *timeseries.TimeRangeQuery,
	client origins.TimeseriesClient) (timeseries.Timeseries, *HTTPDocument, time.Duration, error) {

	rsc := request.GetResources(pr.Request)

	ctx, span := tspan.NewChildSpan(pr.upstreamRequest.Context(), rsc.Tracer, "FetchTimeSeries")
	if span != nil {
		defer span.End()
	}
	pr.upstreamRequest = pr.upstreamRequest.WithContext(ctx)

	body, resp, elapsed := pr.Fetch()

	d := &HTTPDocument{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       body,
	}

	if resp.StatusCode != 200 {
		pr.Logger.Error("unexpected upstream response",
			tl.Pairs{
				"statusCode":              resp.StatusCode,
				"clientRequestURL":        pr.Request.URL.String(),
				"clientRequestMethod":     pr.Request.Method,
				"clientRequestHeaders":    pr.Request.Header,
				"upstreamRequestURL":      pr.upstreamRequest.URL.String(),
				"upstreamRequestMethod":   pr.upstreamRequest.Method,
				"upstreamRequestHeaders":  headers.LogString(pr.upstreamRequest.Header),
				"upstreamResponseHeaders": headers.LogString(resp.Header),
				"upstreamResponseBody":    string(body),
			},
		)
		return nil, d, time.Duration(0), tpe.ErrUnexpectedUpstreamResponse
	}

	ts, err := client.UnmarshalTimeseries(body)
	if err != nil {
		pr.Logger.Error("proxy object unmarshaling failed", tl.Pairs{"body": string(body)})
		return nil, d, time.Duration(0), err
	}

	ts.SetExtents([]timeseries.Extent{trq.Extent})
	ts.SetStep(trq.Step)

	return ts, d, elapsed, nil
}

func recordDPCResult(r *http.Request, cacheStatus status.LookupStatus, httpStatus int, path,
	ffStatus string, elapsed float64, needed []timeseries.Extent, header http.Header) {
	recordResults(r, "DeltaProxyCache", cacheStatus, httpStatus, path, ffStatus, elapsed,
		timeseries.ExtentList(needed), header)
}
