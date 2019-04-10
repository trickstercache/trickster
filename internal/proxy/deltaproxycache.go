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

package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/Comcast/trickster/pkg/locks"
)

// DeltaProxyCacheRequest ...
func DeltaProxyCacheRequest(r *Request, w http.ResponseWriter, client Client, cache cache.Cache, ttl int, refresh bool) {

	trq, err := client.ParseTimeRangeQuery(r)
	if err != nil {
		// err may simply mean incompatible query (e.g., non-select), so just proxy
		ProxyRequest(r, w)
		return
	}
	r.TimeRangeQuery = trq
	trq.NormalizeExtent()

	key := client.DeriveCacheKey(r, r.Headers.Get(hnAuthorization))
	locks.Acquire(key)
	defer locks.Release(key)

	// this is used to determine if Fast Forward should be activated for this request
	normalizedNow := &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: time.Now()},
		Step:   trq.Step,
	}
	normalizedNow.NormalizeExtent()

	cfg := client.Configuration()

	// this is used to ensure the head of the cache respects the BackFill Tolerance
	bf := timeseries.Extent{Start: time.Unix(0, 0), End: normalizedNow.Extent.End}
	if cfg.BackfillToleranceSecs > 0 {
		bf.End = bf.End.Add(time.Duration(-cfg.BackfillToleranceSecs) * time.Second)
	}

	var cts timeseries.Timeseries
	var doc *HTTPDocument
	var elapsed time.Duration

	cacheStatus := crKeyMiss

	if refresh {
		cacheStatus = crPurge
		cts, doc, elapsed, err = fetchTimeseries(r, client)
		if err != nil {
			Respond(w, doc.StatusCode, doc.Headers, doc.Body)
			return // fetchTimeseries logs the error
		}
	} else {
		doc, err = QueryCache(cache, key)
		if err != nil {
			cts, doc, elapsed, err = fetchTimeseries(r, client)
			if err != nil {
				Respond(w, doc.StatusCode, doc.Headers, doc.Body)
				return // fetchTimeseries logs the error
			}
		} else {
			// Load the Cached Timeseries
			cts, err = client.UnmarshalTimeseries(doc.Body)
			if err != nil {
				log.Error("cache object unmarshaling failed", log.Pairs{"key": key, "originName": client.OriginName})
				cts, doc, elapsed, err = fetchTimeseries(r, client)
				if err != nil {
					Respond(w, doc.StatusCode, doc.Headers, doc.Body)
					return // fetchTimeseries logs the error
				}
			} else {
				cacheStatus = crPartialHit
			}
		}
	}

	// On the first load from cache, tell the Cached Timeseries its step
	if cts.Step().Seconds() == 0 {
		cts.SetStep(trq.Step)
	}

	// Find the ranges that we want, but which are not currently cached
	missRanges := trq.CalculateDeltas(cts.Extents())

	if len(missRanges) == 0 {
		if cacheStatus == crPartialHit {
			cacheStatus = crHit
		}
	} else if len(missRanges) == 1 && missRanges[0].Start == trq.Extent.Start && missRanges[0].End == trq.Extent.End {
		cacheStatus = crRangeMiss
	}

	// Increment Counters
	metrics.ProxyRequestStatus.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, cacheStatus, strconv.Itoa(doc.StatusCode), r.URL.Path).Inc()
	metrics.ProxyRequestDuration.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, cacheStatus, strconv.Itoa(doc.StatusCode), r.URL.Path).Observe(elapsed.Seconds())

	var ffURL *url.URL
	// if the step resolution <= Fast Forward TTL, then no need to even try Fast Forward
	cacheConfig := cache.Configuration()
	if int64(trq.Step) > int64(cacheConfig.FastForwardTTLSecs)*int64(time.Second) {
		ffURL, err = client.FastForwardURL(r)
		if err != nil {
			ffURL = nil
		}
	}

	// if it's a cache hit and fast forward is disabled or unsupported, just return the data.
	if cacheStatus == crHit && (cfg.FastForwardDisable || ffURL == nil) {
		Respond(w, doc.StatusCode, doc.Headers, doc.Body)
		return
	}

	// maintain a list of timeseries to merge into the main timeseries
	mts := make([]timeseries.Timeseries, 0, len(missRanges))
	wg := sync.WaitGroup{}
	appendLock := sync.Mutex{}
	var rh http.Header

	uncachedValueCount := 0

	// iterate each time range that the client needs and fetch from the upstream origin
	for i := range missRanges {
		wg.Add(1)
		req := r.Copy() // copy the request headers so we avoid collisions when adjusting them

		// This fetches the gaps from the origin and adds their datasets to the merge list
		go func(e *timeseries.Extent, rq *Request) {
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
				if rh == nil {
					// Use the response headers from the first successful delta request to complete as our downstream response headers
					rh = resp.Header
				}
			}
		}(&missRanges[i], req)
	}

	var hasFastForwardData bool
	var ffts timeseries.Timeseries
	// Only fast forward if configured and the user request is for the absolute latest datapoint
	if ffURL != nil && (!cfg.FastForwardDisable) && (trq.Extent.End == normalizedNow.Extent.End) && ffURL.Scheme != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := r.Copy()
			req.URL = ffURL
			body, resp := FetchViaObjectProxyCache(req, client, cache, cacheConfig.FastForwardTTLSecs, false, true)
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				ffts, err = client.UnmarshalInstantaneous(body)
				if err != nil {
					log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
					return
				}
				x := ffts.Extents()
				if len(x) > 0 {
					hasFastForwardData = x[0].End.After(trq.Extent.End)
				}
			}
		}()
	}

	wg.Wait()

	// Merge the new delta timeseries into the cached timeseries
	if len(mts) > 0 {
		cts.Merge(true, mts...)
	}

	// Get the Request Object, Cropped down from the full Cache
	rts := cts.Crop(trq.Extent)

	cachedValueCount := rts.ValueCount() - uncachedValueCount

	if uncachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(r.OriginName, r.OriginType, "uncached", r.URL.Path).Add(float64(uncachedValueCount))
	}
	if cachedValueCount > 0 {
		metrics.ProxyRequestElements.WithLabelValues(r.OriginName, r.OriginType, "cached", r.URL.Path).Add(float64(cachedValueCount))
	}

	// Merge Fast Forward data if present. This must be done after the Downstream Crop since
	// the cropped extent was normalized to stepboundaries and would remove fast forward data
	if hasFastForwardData {
		rts.Merge(false, ffts)
	}
	rdata, err := client.MarshalTimeseries(rts)

	// Don't write the cache unless it has chnaged
	if cacheStatus != crHit {
		wg.Add(1)
		// Write the newly-merged object back to the cache
		go func() {
			defer wg.Done()
			// Crop the Cached Object down to the Sample Age Retention Policy before storing
			re := timeseries.Extent{End: bf.End, Start: time.Now().Add(-time.Duration(cfg.MaxValueAgeSecs) * time.Second)}
			cts = cts.Crop(re)
			cdata, err := client.MarshalTimeseries(cts)
			if err != nil {
				return
			}
			doc.Body = cdata
			WriteCache(cache, key, doc, ttl)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Respond to the user. Using the response headers from a Delta Response, so as to not map conflict with cacheData on WriteCache
		Respond(w, doc.StatusCode, rh, rdata)
	}()

	wg.Wait()
}

func fetchTimeseries(r *Request, client Client) (timeseries.Timeseries, *HTTPDocument, time.Duration, error) {

	body, resp, elapsed := Fetch(r)

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

	if resp.StatusCode >= 400 {
		elapsed = 0
	}
	return ts, d, elapsed, nil
}
