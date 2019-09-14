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
	"net/http"
	"time"

	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/util/log"

	tc "github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/pkg/locks"
)

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, cache tc.Cache, ttl time.Duration, noLock bool) {
	body, resp, _ := FetchViaObjectProxyCache(r, client, cache, ttl, noLock)
	Respond(w, resp.StatusCode, resp.Header, body)
}

// FetchViaObjectProxyCache Fetches an object from Cache or Origin (on miss), writes the object to the cache, and returns the object to the caller
func FetchViaObjectProxyCache(r *model.Request, client model.Client, cache tc.Cache, ttl time.Duration, noLock bool) ([]byte, *http.Response, bool) {

	cfg := client.Configuration()
	key := cfg.Host + "." + DeriveCacheKey(client, cfg, r, "")

	if !noLock {
		locks.Acquire(key)
		defer locks.Release(key)
	}

	cpReq := GetRequestCachingPolicy(r.Headers)
	if cpReq.NoCache {
		// if the client provided Cache-Control: no-cache or Pragma: no-cache header, the request is proxy only.
		body, resp, _ := Fetch(r)
		cache.Remove(key)
		return body, resp, false
	}

	hasINMV := cpReq.IfNoneMatchValue != ""
	hasIMS := !cpReq.IfModifiedSinceTime.IsZero()
	hasIMV := cpReq.IfMatchValue != ""
	hasIUS := !cpReq.IfUnmodifiedSinceTime.IsZero()
	clientConditional := hasINMV || hasIMS || hasIMV || hasIUS

	// don't proxy these up, their scope is only between Trickster and client
	if clientConditional {
		delete(r.Headers, headers.NameIfMatch)
		delete(r.Headers, headers.NameIfUnmodifiedSince)
		delete(r.Headers, headers.NameIfNoneMatch)
		delete(r.Headers, headers.NameIfModifiedSince)
	}

	revalidatingCache := false

	var cacheStatus = tc.LookupStatusKeyMiss

	d, err := QueryCache(cache, key)
	if err == nil {
		d.CachingPolicy.IsFresh = !d.CachingPolicy.LocalDate.Add(time.Duration(d.CachingPolicy.FreshnessLifetime) * time.Second).Before(time.Now())
		if !d.CachingPolicy.IsFresh {
			if !d.CachingPolicy.CanRevalidate {
				// The cache entry has expired and lacks any data for validating freshness
				cache.Remove(key)
			} else {
				if d.CachingPolicy.ETag != "" {
					r.Headers.Set(headers.NameIfNoneMatch, d.CachingPolicy.ETag)
				}
				if !d.CachingPolicy.LastModified.IsZero() {
					r.Headers.Set(headers.NameIfModifiedSince, d.CachingPolicy.LastModified.Format(time.RFC1123))
				}
				revalidatingCache = true
			}
		}
	}

	var body []byte
	var resp *http.Response
	var elapsed time.Duration

	if d.CachingPolicy != nil && d.CachingPolicy.IsFresh {
		cacheStatus = tc.LookupStatusHit
	} else {

		body, resp, elapsed = Fetch(r)

		cp := GetResponseCachingPolicy(resp.StatusCode, r.OriginConfig.NegativeCache, resp.Header)

		// Cache is revalidated, update headers and resulting caching policy
		if revalidatingCache && resp.StatusCode == http.StatusNotModified {
			for k, v := range resp.Header {
				d.Headers[k] = v
			}
			d.CachingPolicy = cp
			// TODO: update any cache metadata like TTL
		} else {
			d = model.DocumentFromHTTPResponse(resp, body, cp)
		}
	}

	recordOPCResult(r, cacheStatus, d.StatusCode, r.URL.Path, elapsed.Seconds(), d.Headers)

	log.Debug("http object cache lookup status", log.Pairs{"key": key, "cacheStatus": cacheStatus})

	// the client provided a conditional request to us, determine if Trickster responds with 304 or 200
	// based on client-provided validators vs our now-fresh cache
	if clientConditional {
		isClientFresh := true
		if hasINMV {
			// need to do real matching of etag lists - package
			isClientFresh = isClientFresh && d.CachingPolicy.ETag == cpReq.IfNoneMatchValue
		}
		if hasIMV {
			// need to do real matching of etag lists -> package
			isClientFresh = isClientFresh && d.CachingPolicy.ETag != cpReq.IfMatchValue
		}
		if hasIMS {
			isClientFresh = isClientFresh && !d.CachingPolicy.LastModified.After(cpReq.IfModifiedSinceTime)
		}
		if hasIUS {
			isClientFresh = isClientFresh && d.CachingPolicy.LastModified.After(cpReq.IfUnmodifiedSinceTime)
		}
		cpReq.IsFresh = isClientFresh
	}

	d.CachingPolicy.NoTransform = d.CachingPolicy.NoTransform || cpReq.NoTransform
	d.CachingPolicy.NoCache = d.CachingPolicy.NoCache || cpReq.NoCache || len(body) >= cache.Configuration().MaxObjectSizeBytes

	if !d.CachingPolicy.MustRevalidate && !d.CachingPolicy.NoCache && d.CachingPolicy.FreshnessLifetime <= 0 {
		d.CachingPolicy.FreshnessLifetime = int(r.PathConfig.DefaultTTL.Seconds())
	}

	if d.CachingPolicy.NoCache || (!d.CachingPolicy.CanRevalidate && d.CachingPolicy.FreshnessLifetime <= 0) {
		cache.Remove(key)
	} else if !d.CachingPolicy.IsFresh {
		WriteCache(cache, key, d, time.Duration(d.CachingPolicy.FreshnessLifetime)*time.Second)
	} else {
		body = d.Body
		resp = &http.Response{
			Header:     d.Headers,
			StatusCode: d.StatusCode,
			Status:     d.Status,
		}
	}

	if clientConditional && cpReq.IsFresh {
		resp.StatusCode = http.StatusNotModified
		body = nil
	}

	return body, resp, cacheStatus == tc.LookupStatusHit

}

func recordOPCResult(r *model.Request, cacheStatus tc.LookupStatus, httpStatus int, path string, elapsed float64, header http.Header) {
	recordResults(r, "ObjectProxyCache", cacheStatus, httpStatus, path, "", elapsed, nil, header)
}
