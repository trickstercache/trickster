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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/Comcast/trickster/pkg/locks"
)

// DeltaProxyCacheRequest identifies the gaps between the cache and a new timeseries request,
// requests the gaps from the origin server and returns the reconstituted dataset tto the downstream request
// while caching the results for subsequent requests of the same data
func DeltaProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, cache cache.Cache, ttl time.Duration) {

	cfg := client.Configuration()
	r.FastForwardDisable = cfg.FastForwardDisable
	refresh := isRefresh(r.ClientRequest.Header) && !cfg.IgnoreNoCacheHeader

	trq, err := client.ParseTimeRangeQuery(r)
	if err != nil {
		// err may simply mean incompatible query (e.g., non-select), so just proxy
		ProxyRequest(r, w)
		return
	}

	// this is used to ensure the head of the cache respects the BackFill Tolerance
	bf := timeseries.Extent{Start: time.Unix(0, 0), End: trq.Extent.End}
	if !trq.IsOffset && cfg.BackfillTolerance > 0 {
		bf.End = bf.End.Add(-cfg.BackfillTolerance)
	}

	now := time.Now()
	OldestRetainedTimestamp := now.Truncate(trq.Step).Add(-(trq.Step * cfg.ValueRetention))
	if trq.Extent.End.Before(OldestRetainedTimestamp) {
		log.Debug("timerange end is too early to consider caching", log.Pairs{"oldestRetainedTimestamp": OldestRetainedTimestamp, "step": trq.Step, "retention": cfg.ValueRetention})
		ProxyRequest(r, w)
		return
	}

	if trq.Extent.Start.After(bf.End) {
		log.Debug("timerange is too new to cache due to backfill tolerance", log.Pairs{"backFillToleranceSecs": cfg.BackfillToleranceSecs, "newestRetainedTimestamp": bf.End, "queryStart": trq.Extent.Start})
		ProxyRequest(r, w)
		return
	}

	r.TimeRangeQuery = trq
	trq.NormalizeExtent()
	client.SetExtent(r, &trq.Extent)

	key := cfg.Host + "." + client.DeriveCacheKey(r, r.Headers.Get(headers.NameAuthorization))
	locks.Acquire(key)
	defer locks.Release(key)

	// this is used to determine if Fast Forward should be activated for this request
	normalizedNow := &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: now},
		Step:   trq.Step,
	}
	normalizedNow.NormalizeExtent()

	var cts timeseries.Timeseries
	var doc *model.HTTPDocument
	var elapsed time.Duration

	cacheStatus := crKeyMiss

	if refresh {
		cacheStatus = crPurge
		cache.Remove(key)
		cts, doc, elapsed, err = fetchTimeseries(r, client)
		if err != nil {
			recordDPCResult(r, "proxy-error", strconv.Itoa(doc.StatusCode), r.URL.Path, "", elapsed.Seconds(), nil, doc.Headers)
			Respond(w, doc.StatusCode, doc.Headers, doc.Body)
			return // fetchTimeseries logs the error
		}
	} else {
		doc, err = QueryCache(cache, key)
		if err != nil {
			cts, doc, elapsed, err = fetchTimeseries(r, client)
			if err != nil {
				recordDPCResult(r, "proxy-error", strconv.Itoa(doc.StatusCode), r.URL.Path, "", elapsed.Seconds(), nil, doc.Headers)
				Respond(w, doc.StatusCode, doc.Headers, doc.Body)
				return // fetchTimeseries logs the error
			}
		} else {
			// Load the Cached Timeseries
			cts, err = client.UnmarshalTimeseries(doc.Body)
			if err != nil {
				log.Error("cache object unmarshaling failed", log.Pairs{"key": key, "originName": client.Name()})
				cache.Remove(key)
				cts, doc, elapsed, err = fetchTimeseries(r, client)
				if err != nil {
					recordDPCResult(r, "proxy-error", strconv.Itoa(doc.StatusCode), r.URL.Path, "", elapsed.Seconds(), nil, doc.Headers)
					Respond(w, doc.StatusCode, doc.Headers, doc.Body)
					return // fetchTimeseries logs the error
				}
			} else {
				cacheStatus = crPartialHit
			}
		}
	}

	// Find the ranges that we want, but which are not currently cached
	var missRanges []timeseries.Extent
	if cacheStatus == crPartialHit {
		missRanges = trq.CalculateDeltas(cts.Extents())
	}

	if len(missRanges) == 0 && cacheStatus == crPartialHit {
		cacheStatus = crHit
	} else if len(missRanges) == 1 && missRanges[0].Start == trq.Extent.Start && missRanges[0].End == trq.Extent.End {
		cacheStatus = crRangeMiss
	}

	ffStatus := "off"

	var ffURL *url.URL
	// if the step resolution <= Fast Forward TTL, then no need to even try Fast Forward
	cacheConfig := cache.Configuration()
	if !r.FastForwardDisable {
		if trq.Step > cacheConfig.FastForwardTTL {
			ffURL, err = client.FastForwardURL(r)
			if err != nil || ffURL == nil {
				ffStatus = "err"
				r.FastForwardDisable = true
			}
		} else {
			r.FastForwardDisable = true
		}
	}

	dpStatus := log.Pairs{"cacheKey": key, "cacheStatus": cacheStatus, "reqStart": trq.Extent.Start, "reqEnd": trq.Extent.End}
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
		req := r.Copy() // copy the request headers so we avoid collisions when adjusting them

		// This fetches the gaps from the origin and adds their datasets to the merge list
		go func(e *timeseries.Extent, rq *model.Request) {
			defer wg.Done()
			client.SetExtent(rq, e)
			body, resp, _ := Fetch(rq)
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, err := client.UnmarshalTimeseries(body)
				if err != nil {
					log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
					return
				}
				uncachedValueCount += nts.ValueCount()
				nts.SetExtents([]timeseries.Extent{*e})
				appendLock.Lock()
				defer appendLock.Unlock()

				mts = append(mts, nts)
			}
		}(&missRanges[i], req)
	}

	var hasFastForwardData bool
	var ffts timeseries.Timeseries
	// Only fast forward if configured and the user request is for the absolute latest datapoint

	if (!r.FastForwardDisable) && (trq.Extent.End == normalizedNow.Extent.End) && ffURL.Scheme != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := r.Copy()
			req.URL = ffURL
			body, resp, isHit := FetchViaObjectProxyCache(req, client, cache, cacheConfig.FastForwardTTL, false, true)
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				ffts, err = client.UnmarshalInstantaneous(body)
				if err != nil {
					ffStatus = "err"
					log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
					return
				}
				ffts.SetStep(trq.Step)
				fmt.Println("calling ffts extents")
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
		elapsed = time.Now().Sub(now)
		cts.Merge(true, mts...)
	}

	rts := cts.Copy()
	if cacheStatus != crKeyMiss {
		rts.Crop(trq.Extent)
	}
	cachedValueCount := rts.ValueCount() - uncachedValueCount

	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(r.OriginName, r.OriginType, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}

	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(r.OriginName, r.OriginType, "cached", r.URL.Path).Add(float64(cachedValueCount))
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
	rh := headers.CopyHeaders(doc.Headers)

	switch cacheStatus {
	case crKeyMiss, crPartialHit, crRangeMiss:
		wg.Add(1)
		// Write the newly-merged object back to the cache
		go func() {
			defer wg.Done()
			// Crop the Cache Object down to the Sample Age Retention Policy and the Backfill Tolerance before storing to cache
			cts.Crop(timeseries.Extent{End: bf.End, Start: OldestRetainedTimestamp})
			// Don't cache empty datasets, ensure there is at least 1 value (e.g., all of your cached time is in the backfill tolerance)
			if cts.ValueCount() > 0 {
				cdata, err := client.MarshalTimeseries(cts)
				if err != nil {
					return
				}
				doc.Body = cdata
				WriteCache(cache, key, doc, ttl)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Respond to the user. Using the response headers from a Delta Response, so as to not map conflict with cacheData on WriteCache
		logDeltaRoutine(dpStatus)
		recordDPCResult(r, cacheStatus, strconv.Itoa(doc.StatusCode), r.URL.Path, ffStatus, elapsed.Seconds(), missRanges, rh)
		Respond(w, doc.StatusCode, rh, rdata)
	}()

	wg.Wait()
}

func logDeltaRoutine(p log.Pairs) { log.Debug("delta routine completed", p) }

func fetchTimeseries(r *model.Request, client model.Client) (timeseries.Timeseries, *model.HTTPDocument, time.Duration, error) {
	body, resp, elapsed := Fetch(r)

	d := &model.HTTPDocument{
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

	ts.SetExtents([]timeseries.Extent{r.TimeRangeQuery.Extent})
	ts.SetStep(r.TimeRangeQuery.Step)

	return ts, d, elapsed, nil
}

func recordDPCResult(r *model.Request, cacheStatus, httpStatus, path, ffStatus string, elapsed float64, needed []timeseries.Extent, header http.Header) {
	metrics.ProxyRequestStatus.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, cacheStatus, httpStatus, path).Inc()
	if elapsed > 0 {
		metrics.ProxyRequestDuration.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, cacheStatus, httpStatus, path).Observe(elapsed)
	}
	headers.SetResultsHeader(header, "DeltaProxyCache", cacheStatus, ffStatus, timeseries.ExtentList(needed))
}
