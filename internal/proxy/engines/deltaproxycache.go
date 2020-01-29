/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/cache/status"
	"github.com/Comcast/trickster/internal/config"
	tctx "github.com/Comcast/trickster/internal/proxy/context"
	"github.com/Comcast/trickster/internal/proxy/origins"
	"github.com/Comcast/trickster/internal/proxy/request"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/Comcast/trickster/internal/util/tracing"
	"github.com/Comcast/trickster/pkg/locks"
)

// DeltaProxyCache is used for Time Series Acceleration, and not used for normal HTTP Object Caching

// DeltaProxyCacheRequest identifies the gaps between the cache and a new timeseries request,
// requests the gaps from the origin server and returns the reconstituted dataset to the downstream request
// while caching the results for subsequent requests of the same data
func DeltaProxyCacheRequest(w http.ResponseWriter, r *http.Request) {

	rsc := request.GetResources(r)
	oc := rsc.OriginConfig

	ctx, span := tracing.NewChildSpan(r.Context(), oc.TracingConfig.Tracer, "DeltaProxyCacheRequest")
	defer span.End()

	pc := rsc.PathConfig
	cache := rsc.CacheClient
	cc := rsc.CacheConfig

	client := rsc.OriginClient.(origins.TimeseriesClient)

	trq, err := client.ParseTimeRangeQuery(r)
	if err != nil {
		// err may simply mean incompatible query (e.g., non-select), so just proxy
		DoProxy(w, r)
		return
	}

	var cacheStatus status.LookupStatus

	pr := newProxyRequest(r, w)
	trq.FastForwardDisable = oc.FastForwardDisable || trq.FastForwardDisable
	trq.NormalizeExtent()

	// this is used to ensure the head of the cache respects the BackFill Tolerance
	bf := timeseries.Extent{Start: time.Unix(0, 0), End: trq.Extent.End}

	if !trq.IsOffset && oc.BackfillTolerance > 0 {
		bf.End = bf.End.Add(-oc.BackfillTolerance)
	}

	now := time.Now()

	OldestRetainedTimestamp := time.Time{}
	if oc.TimeseriesEvictionMethod == config.EvictionMethodOldest {
		OldestRetainedTimestamp = now.Truncate(trq.Step).Add(-(trq.Step * oc.TimeseriesRetention))
		if trq.Extent.End.Before(OldestRetainedTimestamp) {
			log.Debug("timerange end is too early to consider caching", log.Pairs{"oldestRetainedTimestamp": OldestRetainedTimestamp, "step": trq.Step, "retention": oc.TimeseriesRetention})
			DoProxy(w, r)
			return
		}
		if trq.Extent.Start.After(bf.End) {
			log.Debug("timerange is too new to cache due to backfill tolerance", log.Pairs{"backFillToleranceSecs": oc.BackfillToleranceSecs, "newestRetainedTimestamp": bf.End, "queryStart": trq.Extent.Start})
			DoProxy(w, r)
			return
		}
	}

	client.SetExtent(r, trq, &trq.Extent)
	key := oc.CacheKeyPrefix + "." + pr.DeriveCacheKey(trq.TemplateURL, "")

	locks.Acquire(key)

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
	if coReq.NoCache {
		span.AddEvent(
			ctx,
			"Not Caching",
		)
		cacheStatus = status.LookupStatusPurge
		cache.Remove(key)
		cts, doc, elapsed, err = fetchTimeseries(pr, trq, client)
		if err != nil {
			recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode, r.URL.Path, "", elapsed.Seconds(), nil, doc.Headers)
			Respond(w, doc.StatusCode, doc.Headers, doc.Body)
			locks.Release(key)
			return // fetchTimeseries logs the error
		}
	} else {
		doc, cacheStatus, _, err = QueryCache(ctx, cache, key, nil)
		if cacheStatus == status.LookupStatusKeyMiss {
			cts, doc, elapsed, err = fetchTimeseries(pr, trq, client)
			if err != nil {
				recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode, r.URL.Path, "", elapsed.Seconds(), nil, doc.Headers)

				Respond(w, doc.StatusCode, doc.Headers, doc.Body)
				locks.Release(key)
				return // fetchTimeseries logs the error
			}
		} else {

			// Load the Cached Timeseries
			if doc == nil {
				err = errors.New("empty document body")
			} else {
				if cc.CacheType == "memory" {
					cts = doc.timeseries
				} else {
					cts, err = client.UnmarshalTimeseries(doc.Body)
				}
			}
			if err != nil {
				log.Error("cache object unmarshaling failed", log.Pairs{"key": key, "originName": client.Name()})
				cache.Remove(key)
				cts, doc, elapsed, err = fetchTimeseries(pr, trq, client)
				if err != nil {
					recordDPCResult(r, status.LookupStatusProxyError, doc.StatusCode, r.URL.Path, "", elapsed.Seconds(), nil, doc.Headers)
					Respond(w, doc.StatusCode, doc.Headers, doc.Body)
					locks.Release(key)
					return // fetchTimeseries logs the error
				}
			} else {
				if oc.TimeseriesEvictionMethod == config.EvictionMethodLRU {
					el := cts.Extents()
					tsc := cts.TimestampCount()
					if tsc > 0 &&
						tsc >= oc.TimeseriesRetentionFactor {
						if trq.Extent.End.Before(el[0].Start) {
							log.Debug("timerange end is too early to consider caching", log.Pairs{"step": trq.Step, "retention": oc.TimeseriesRetention})
							locks.Release(key)
							DoProxy(w, r)
							return
						}
						if trq.Extent.Start.After(el[len(el)-1].End) {
							log.Debug("timerange is too new to cache due to backfill tolerance", log.Pairs{"backFillToleranceSecs": oc.BackfillToleranceSecs, "newestRetainedTimestamp": bf.End, "queryStart": trq.Extent.Start})
							locks.Release(key)
							DoProxy(w, r)
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
		// on full cache hit, elapsed records the time taken to query the cache and definitively conclude that it is a full cache hit
		elapsed = time.Since(now)
		cacheStatus = status.LookupStatusHit
	} else if len(missRanges) == 1 && missRanges[0].Start.Equal(trq.Extent.Start) && missRanges[0].End.Equal(trq.Extent.End) {
		cacheStatus = status.LookupStatusRangeMiss
	}

	ffStatus := "off"

	var ffURL *url.URL
	// if the step resolution <= Fast Forward TTL, then no need to even try Fast Forward
	if !trq.FastForwardDisable {
		if trq.Step > oc.FastForwardTTL {
			ffURL, err = client.FastForwardURL(r)
			if err != nil || ffURL == nil {
				ffStatus = "err"
				trq.FastForwardDisable = true
			}
		} else {
			trq.FastForwardDisable = true
		}
	}

	dpStatus := log.Pairs{"cacheKey": key, "cacheStatus": cacheStatus, "reqStart": trq.Extent.Start.Unix(), "reqEnd": trq.Extent.End.Unix()}
	if len(missRanges) > 0 {
		dpStatus["extentsFetched"] = timeseries.ExtentList(missRanges).String()
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
			rq.Request = rq.WithContext(tctx.WithResources(r.Context(), request.NewResources(oc, pc, cc, cache, client)))
			client.SetExtent(rq.Request, trq, e)
			body, resp, _ := rq.Fetch()
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, err := client.UnmarshalTimeseries(body)
				if err != nil {
					log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
					return
				}
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
	if (!trq.FastForwardDisable) && (trq.Extent.End.Equal(normalizedNow.Extent.End)) && ffURL.Scheme != "" {

		wg.Add(1)
		rs := request.NewResources(oc, oc.FastForwardPath, cc, cache, client)
		rs.AlternateCacheTTL = oc.FastForwardTTL
		req := r.Clone(tctx.WithResources(context.Background(), rs))
		go func() {
			defer wg.Done()
			_, span := tracing.NewChildSpan(ctx, oc.TracingConfig.Tracer, "FastForward")
			defer span.End()

			// create a new context that uses the fast forward path config instead of the time series path config
			req.URL = ffURL
			body, resp, isHit := FetchViaObjectProxyCache(req)
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				ffts, err = client.UnmarshalInstantaneous(body)
				if err != nil {
					ffStatus = "err"
					log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
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
		// on a partial hit, elapsed should record the amount of time waiting for all upstream requests to complete
		elapsed = time.Since(now)
		cts.Merge(true, mts...)
	}

	// cts is the cacheable time series, rts is the user's response timeseries
	rts := cts.Clone()

	// if it was a cache key miss, there is no need to undergo Crop since the extents are identical
	if cacheStatus != status.LookupStatusKeyMiss {
		rts.CropToRange(trq.Extent)
	}
	cachedValueCount := rts.ValueCount() - uncachedValueCount

	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(oc.Name, oc.OriginType, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}

	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(oc.Name, oc.OriginType, "cached", r.URL.Path).Add(float64(cachedValueCount))
	}

	// Merge Fast Forward data if present. This must be done after the Downstream Crop since
	// the cropped extent was normalized to stepboundaries and would remove fast forward data
	// If the fast forward data point is older (e.g. cached) than the last datapoint in the returned time series, it will not be merged
	if hasFastForwardData && len(ffts.Extents()) == 1 && ffts.Extents()[0].Start.Truncate(time.Second).After(normalizedNow.Extent.End) {
		rts.Merge(false, ffts)
	}
	rts.SetExtents(nil) // so they are not included in the client response json
	rts.SetStep(0)
	rdata, err := client.MarshalTimeseries(rts)
	rh := http.Header(doc.Headers).Clone()

	switch cacheStatus {
	case status.LookupStatusKeyMiss, status.LookupStatusPartialHit, status.LookupStatusRangeMiss:
		wg.Add(1)
		// Write the newly-merged object back to the cache
		go func() {
			defer wg.Done()
			// Crop the Cache Object down to the Sample Size or Age Retention Policy and the Backfill Tolerance before storing to cache
			switch oc.TimeseriesEvictionMethod {
			case config.EvictionMethodLRU:
				cts.CropToSize(oc.TimeseriesRetentionFactor, bf.End, trq.Extent)
			default:
				cts.CropToRange(timeseries.Extent{End: bf.End, Start: OldestRetainedTimestamp})
			}
			// Don't cache datasets with empty extents (everything was cropped so there is nothing to cache)
			if len(cts.Extents()) > 0 {
				if cc.CacheType == "memory" {
					doc.timeseries = cts
				} else {
					cdata, err := client.MarshalTimeseries(cts)
					if err != nil {
						locks.Release(key)
						return
					}
					doc.Body = cdata
				}
				WriteCache(ctx, cache, key, doc, oc.TimeseriesTTL, oc.CompressableTypes)
			}
		}()
	}

	// Respond to the user. Using the response headers from a Delta Response, so as to not map conflict with cacheData on WriteCache
	logDeltaRoutine(dpStatus)
	recordDPCResult(r, cacheStatus, doc.StatusCode, r.URL.Path, ffStatus, elapsed.Seconds(), missRanges, rh)
	Respond(w, doc.StatusCode, rh, rdata)

	wg.Wait()
	locks.Release(key)
}

func logDeltaRoutine(p log.Pairs) { log.Debug("delta routine completed", p) }

func fetchTimeseries(pr *proxyRequest, trq *timeseries.TimeRangeQuery, client origins.TimeseriesClient) (timeseries.Timeseries, *HTTPDocument, time.Duration, error) {

	body, resp, elapsed := pr.Fetch()

	d := &HTTPDocument{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       body,
	}

	if resp.StatusCode != 200 {
		log.Error("unexpected upstream response", log.Pairs{"statusCode": resp.StatusCode})
		return nil, d, time.Duration(0), fmt.Errorf("Unexpected Upstream Response")
	}

	ts, err := client.UnmarshalTimeseries(body)
	if err != nil {
		log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
		return nil, d, time.Duration(0), err
	}

	ts.SetExtents([]timeseries.Extent{trq.Extent})
	ts.SetStep(trq.Step)

	return ts, d, elapsed, nil
}

func recordDPCResult(r *http.Request, cacheStatus status.LookupStatus, httpStatus int, path, ffStatus string, elapsed float64, needed []timeseries.Extent, header http.Header) {
	recordResults(r, "DeltaProxyCache", cacheStatus, httpStatus, path, ffStatus, elapsed, timeseries.ExtentList(needed), header)
}
