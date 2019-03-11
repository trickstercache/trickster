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
	"net/http"
	"sync"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

// DeltaProxyCacheRequest ...
func DeltaProxyCacheRequest(r *Request, w http.ResponseWriter, client Client, cache cache.Cache, ttl int, refresh bool) {

	status := crKeyMiss
	key := client.DeriveCacheKey(r.URL.Path, r.URL.Query(), "", "")

	locks.Acquire(key)
	defer locks.Release(key)

	trq, err := client.ParseTimeRangeQuery(r.ClientRequest)
	if err != nil {
		log.Error("parse timerange query failed", log.Pairs{"error": err})
		ProxyRequest(r, w)
		return
	}

	status = crPartialHit

	trq.NormalizeExtent()

	if refresh {
		ObjectProxyCacheRequest(r, w, client, cache, ttl, true, true)
		return
	}

	cacheData, err := QueryCache(cache, key)
	if err != nil {
		// cache miss
		ObjectProxyCacheRequest(r, w, client, cache, ttl, true, true)
		return
	}

	// Load the Cached Timeseries
	cts, err := client.UnmarshalTimeseries(cacheData.Body)
	if err != nil {
		log.Error("cache object unmarshaling failed", log.Pairs{"key": key, "originName": client.OriginName})
		ObjectProxyCacheRequest(r, w, client, cache, ttl, true, true)
		return
	}

	// On the first load from cache, tell the Cached Timeseries its step
	if cts.Step().Seconds() == 0 {
		cts.SetStep(time.Duration(trq.Step) * time.Second)
	}

	// Find the ranges that we want, but which are not currently cached
	missRanges := cts.CalculateDeltas(trq)

	if len(missRanges) == 0 {
		status = crHit
		Respond(w, cacheData.StatusCode, cacheData.Headers, cacheData.Body)
		return
	}

	// maintain a list of timeseries to merge into the main timeseries
	mts := make([]timeseries.Timeseries, 0, len(missRanges))
	wg := sync.WaitGroup{}
	appendLock := sync.Mutex{}
	var rh http.Header
	for i := range missRanges {
		wg.Add(1)
		req := r.Copy() // copy the request headers so we avoid collisions when adjusting them
		// This fetches the gaps from the origin and adds their datasets to the merge list
		go func(e *timeseries.Extent, r *Request) {
			defer wg.Done()
			client.SetExtent(req, e)
			body, resp, _ := Fetch(req)
			if resp.StatusCode == http.StatusOK && len(body) > 0 {
				nts, err := client.UnmarshalTimeseries(body)
				if err != nil {
					log.Error("proxy object unmarshaling failed", log.Pairs{"body": string(body)})
					return
				}

				nts.SetExtents([]timeseries.Extent{*e})

				appendLock.Lock()
				defer appendLock.Unlock()
				mts = append(mts, nts)
				if rh == nil {
					rh = resp.Header
				}
			}
		}(&missRanges[i], req)
	}

	// Fast Forward Here, Another wg.Add(1), go func, defer wg.Done(), fetch, Matrix from Vector, lock, defer unlock, append to mts

	wg.Wait()
	cts.Merge(mts...)

	// Get the Request Object, Cropped down from the full Cache
	rts := cts.Crop(trq.Extent)
	rdata, err := client.MarshalTimeseries(rts)

	wg.Add(1)
	// Write to cache in
	go func() {
		// Crop the Cached Object down to the Sample Age Retention Policy before storing
		re := timeseries.Extent{End: time.Now(), Start: time.Now().Add(-time.Duration(client.Configuration().MaxValueAgeSecs) * time.Second)}
		cts.Crop(re)
		cdata, err := client.MarshalTimeseries(cts)
		if err != nil {
			return
		}
		cacheData.Body = cdata
		WriteCache(cache, key, cacheData, ttl)
		wg.Done()
	}()

	// Respond to the user. Using the response headers from a Delta Response, so as to not map conflict with cacheData on WriteCache
	Respond(w, cacheData.StatusCode, rh, rdata)
	wg.Wait()
	log.Debug("placeholder status message", log.Pairs{"status": status})
}
