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
	"bytes"
	"io"
	"net/http"
	"time"

	tc "github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, noLock bool) {
	FetchAndRespondViaObjectProxyCache(r, w, client, noLock)
}

// StreamObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache with io streaming
func SequentialObjectProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, noLock bool) {
	body, resp, _ := FetchViaObjectProxyCache(r, client, nil, noLock)
	Respond(w, resp.StatusCode, resp.Header, body)
}

// FetchViaObjectProxyCache Fetches an object from Cache or Origin (on miss), writes the object to the cache, and returns the object to the caller
func FetchViaObjectProxyCache(r *model.Request, client model.Client, apc *config.PathConfig, noLock bool) ([]byte, *http.Response, bool) {

	oc := context.OriginConfig(r.ClientRequest.Context())
	cache := context.CacheClient(r.ClientRequest.Context())

	key := oc.Host + "." + DeriveCacheKey(r, apc, "")

	if !noLock {
		locks.Acquire(key)
		defer locks.Release(key)
	}

	cpReq := GetRequestCachingPolicy(r.Headers)
	if cpReq.NoCache {
		// if the client provided Cache-Control: no-cache or Pragma: no-cache header, the request is proxy only.
		body, resp, elapsed := Fetch(r)
		cache.Remove(key)
		recordOPCResult(r, tc.LookupStatusProxyOnly, resp.StatusCode, r.URL.Path, elapsed.Seconds(), resp.Header)
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

	statusCode := d.StatusCode

	if d.CachingPolicy != nil && d.CachingPolicy.IsFresh {
		cacheStatus = tc.LookupStatusHit
	} else {
		body, resp, elapsed = Fetch(r)
		cp := GetResponseCachingPolicy(resp.StatusCode, oc.NegativeCache, resp.Header)
		// Cache is revalidated, update headers and resulting caching policy
		if revalidatingCache && resp.StatusCode == http.StatusNotModified {
			cacheStatus = tc.LookupStatusHit
			for k, v := range resp.Header {
				d.Headers[k] = v
			}
			d.CachingPolicy = cp
			statusCode = 304
		} else {
			d = model.DocumentFromHTTPResponse(resp, body, cp)
		}
	}

	recordOPCResult(r, cacheStatus, statusCode, r.URL.Path, elapsed.Seconds(), d.Headers)

	log.Info("http object cache lookup status", log.Pairs{"key": key, "cacheStatus": cacheStatus})

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
	d.CachingPolicy.NoCache = d.CachingPolicy.NoCache || cpReq.NoCache || len(body) >= oc.MaxObjectSizeBytes

	if d.CachingPolicy.NoCache || (!d.CachingPolicy.CanRevalidate && d.CachingPolicy.FreshnessLifetime <= 0) {
		cache.Remove(key)
	} else if !d.CachingPolicy.IsFresh {
		var ttl time.Duration = time.Duration(d.CachingPolicy.FreshnessLifetime) * time.Second
		if d.CachingPolicy.CanRevalidate {
			ttl *= time.Duration(oc.RevalidationFactor)
		}
		if ttl > oc.MaxTTL {
			ttl = oc.MaxTTL
		}
		WriteCache(cache, key, d, ttl)
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

// FetchAndRespondViaObjectProxyCache Fetches an object from Cache or Origin (on miss), writes the object to the cache, and returns the object to the caller
func FetchAndRespondViaObjectProxyCache(r *model.Request, w http.ResponseWriter, client model.Client, noLock bool) {

	oc := context.OriginConfig(r.ClientRequest.Context())
	pc := context.PathConfig(r.ClientRequest.Context())
	cache := context.CacheClient(r.ClientRequest.Context())

	key := oc.Host + "." + DeriveCacheKey(r, pc, "")

	cpReq := GetRequestCachingPolicy(r.Headers)
	pcfResult, pcfExists := Reqs.Load(key)
	if pcfExists || cpReq.NoCache {
		// if the client provided Cache-Control: no-cache or Pragma: no-cache header, the request is proxy only.
		start := time.Now()
		// This will use the header from the original response
		resp := ProxyRequest(r, w)
		recordOPCResult(r, tc.LookupStatusProxyOnly, resp.StatusCode, r.URL.Path, time.Since(start).Seconds(), resp.Header)

		locks.Acquire(key)
		cache.Remove(key)
		locks.Release(key)
		return
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

	if !noLock {
		locks.Acquire(key)
		defer locks.Release(key)
	}

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

	var resp *http.Response
	var reader io.Reader
	cl := 0

	statusCode := d.StatusCode

	if d.CachingPolicy != nil && d.CachingPolicy.IsFresh {
		cacheStatus = tc.LookupStatusHit
		cl = len(d.Body)
	} else {
		reader, resp, cl = PrepareFetchReader(r)
		cp := GetResponseCachingPolicy(resp.StatusCode, oc.NegativeCache, resp.Header)
		// Cache is revalidated, update headers and resulting caching policy
		if revalidatingCache && resp.StatusCode == http.StatusNotModified {
			cacheStatus = tc.LookupStatusHit
			for k, v := range resp.Header {
				d.Headers[k] = v
			}
			d.CachingPolicy = cp
			statusCode = 304
		} else {
			// TODO set body
			d = model.DocumentFromHTTPResponse(resp, nil, cp)
		}
	}

	log.Info("http object cache lookup status", log.Pairs{"key": key, "cacheStatus": cacheStatus})

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
	d.CachingPolicy.NoCache = d.CachingPolicy.NoCache || cpReq.NoCache || cl >= oc.MaxObjectSizeBytes

	writeCache := false
	// Where the magic should happen
	if d.CachingPolicy.NoCache || (!d.CachingPolicy.CanRevalidate && d.CachingPolicy.FreshnessLifetime <= 0) {
		cache.Remove(key)

		// is fresh, and we can cache, can revalidate and the freshness is greater than 0
	} else if d.CachingPolicy.IsFresh {
		reader = bytes.NewReader(d.Body)
		resp = &http.Response{
			Header:     d.Headers,
			StatusCode: d.StatusCode,
			Status:     d.Status,
		}

		// is NOT fresh, and we can cache, can revalidate and the freshness is greater than 0
	} else {
		writeCache = true
	}

	// Write body here
	var elapsed time.Duration
	var body []byte

	if clientConditional && cpReq.IsFresh {
		resp.StatusCode = http.StatusNotModified
		reader = bytes.NewBuffer([]byte{})
	}

	headers.SetResultsHeader(resp.Header, "ObjectProxyCache", cacheStatus.String(), "", nil)
	if cacheStatus == tc.LookupStatusKeyMiss {
		start := time.Now()
		if pc != nil && pc.CollapsedForwardingType != config.CFTypeProgressive {
			writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
			buffer := bytes.NewBuffer(make([]byte, 0, cl))
			mw := io.MultiWriter(writer, buffer)
			io.Copy(mw, reader)
			body = buffer.Bytes()
		} else {
			if !pcfExists {
				cl := 0
				reader, resp, cl = PrepareFetchReader(r)
				headers.SetResultsHeader(resp.Header, "ObjectProxyCache", cacheStatus.String(), "", nil)
				writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
				// Check if we know the content length and if it is less than our max object size.
				if cl != 0 && cl < oc.MaxObjectSizeBytes {
					pcf := NewPCF(resp, cl)
					Reqs.Store(key, pcf)

					go func() {
						io.Copy(pcf, reader)
						pcf.Close()
						Reqs.Delete(key)
					}()
					pcf.AddClient(writer)
					// Only record body from original server request
					body, _ = pcf.GetBody()
				}
			} else {
				pcf, _ := pcfResult.(ProgressiveCollapseForwarder)
				resp = pcf.GetResp()
				writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
				pcf.AddClient(writer)
				return
			}
		}
		elapsed = time.Since(start)
	} else {
		writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
		io.Copy(writer, reader)
	}

	recordOPCResult(r, cacheStatus, statusCode, r.URL.Path, elapsed.Seconds(), d.Headers)

	if writeCache {
		if body != nil {
			d.Body = body
		}

		var ttl time.Duration = time.Duration(d.CachingPolicy.FreshnessLifetime) * time.Second
		if d.CachingPolicy.CanRevalidate {
			ttl *= time.Duration(oc.RevalidationFactor)
		}
		if ttl > oc.MaxTTL {
			ttl = oc.MaxTTL
		}
		WriteCache(cache, key, d, ttl)
	}
	return
}

func recordOPCResult(r *model.Request, cacheStatus tc.LookupStatus, httpStatus int, path string, elapsed float64, header http.Header) {
	recordResults(r, "ObjectProxyCache", cacheStatus, httpStatus, path, "", elapsed, nil, header)
}
