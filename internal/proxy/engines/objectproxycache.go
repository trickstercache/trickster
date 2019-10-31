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
	"sync"
	"sync/atomic"
	"time"

	tc "github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

/*
Get caching policy
if can cache and is in cache then respond
if can cache and is not in cache then Proxy/PFC and write result into cache
*/

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, noLock bool) {
	FetchAndRespondViaObjectProxyCache(r, w, client, noLock)
	//FetchViaObjectProxyCache(r, client, nil, noLock)
	//Respond(w, resp.StatusCode, resp.Header, body)

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
	pfcResult, pfcExists := Reqs.Load(key)
	if pfcExists || cpReq.NoCache {
		// if the client provided Cache-Control: no-cache or Pragma: no-cache header, the request is proxy only.
		start := time.Now()
		ProxyRequest(r, w)
		pfc := pfcResult.(ProxyForwardCollapser)
		recordOPCResult(r, tc.LookupStatusProxyOnly, pfc.GetResp().StatusCode, r.URL.Path, time.Since(start).Seconds(), pfc.GetResp().Header)
		locks.Acquire(key)
		cache.Remove(key)
		locks.Release(key)
		return
	}

	if !noLock {
		locks.Acquire(key)
		defer locks.Release(key)
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

	if clientConditional && cpReq.IsFresh {
		resp.StatusCode = http.StatusNotModified
		reader = nil
	}

	// Write body here
	var elapsed time.Duration
	var body []byte

	if cacheStatus == tc.LookupStatusKeyMiss {
		start := time.Now()
		if !pc.ProgressiveCollapsedForwarding {
			writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
			buffer := bytes.NewBuffer(make([]byte, 0, cl))
			mw := io.MultiWriter(writer, buffer)
			io.Copy(mw, reader)
			body = buffer.Bytes()
		} else {
			var n int64
			if !pfcExists {
				cl := 0
				reader, resp, cl = PrepareFetchReader(r)
				writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
				// Check if we know the content length and if it is less than our max object size.
				if cl != 0 && cl < oc.MaxObjectSizeBytes {
					pfc := NewPFC(10*time.Second, resp, cl)
					go pfc.AddClient(writer)
					Reqs.Store(key, pfc)
					// Blocks until server completes
					n, _ = io.Copy(pfc, reader)
					pfc.Close()
					Reqs.Delete(key)
					// Only record body from original server request
					body = pfc.GetBody(uint64(n))
				}
			} else {
				pfc, _ := pfcResult.(ProxyForwardCollapser)
				resp = pfc.GetResp()
				writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
				pfc.AddClient(writer)
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

/* WIP

TODO:
Add maxfilesize option
Move maxfilesize to a per origin basis
Implement this on the object proxy cache
Write unit tests
Write documentation

*/

type indexReader func(index uint64, b []byte) (int, error)

func readAndRespondFromPFC(ir indexReader, w io.Writer) {
	var readIndex uint64
	var err error
	n := 0
	buf := make([]byte, HTTPBlockSize)

	for {
		n, err = ir(readIndex, buf)
		if n > 0 {
			w.Write(buf[:n])
			readIndex++
		}
		if err != nil {
			break
		}
	}
}

// ProxyForwardCollapser accepts data written through the io.Writer interface, caches it and
// makes all the data written available to n readers. The readers can request data at index i,
// to which the PFC may block or return the data immediately.
type ProxyForwardCollapser interface {
	AddClient(io.Writer)
	Write([]byte) (int, error)
	Close()
	Read(uint64, []byte) (int, error)
	WaitServerComplete()
	WaitAllComplete()
	GetBody(n uint64) []byte
	GetResp() *http.Response
}

type proxyForwardCollapser struct {
	resp            *http.Response
	rIndex          uint64
	dataIndex       uint64
	data            [][]byte
	dataStore       []byte
	readCond        *sync.Cond
	serverReadDone  int32
	clientWaitgroup *sync.WaitGroup
	serverWaitCond  *sync.Cond
}

// NewPFC returns a new instance of a ProxyForwardCollapser
func NewPFC(clientTimeout time.Duration, resp *http.Response, contentLength int) ProxyForwardCollapser {
	// This contiguous block of memory is just an underlying byte store, references by the slices defined in refs
	// Thread safety is provided through a read index, an atomic, which the writer must exceed and readers may not exceed
	// This effectively limits the readers and writer to seperate areas in memory.
	dataStore := make([]byte, contentLength)
	refs := make([][]byte, (contentLength/HTTPBlockSize)*2)

	var wg sync.WaitGroup
	sd := sync.NewCond(&sync.Mutex{})
	rc := sync.NewCond(&sync.Mutex{})

	pfc := &proxyForwardCollapser{
		resp:            resp,
		rIndex:          0,
		dataIndex:       0,
		data:            refs,
		dataStore:       dataStore,
		readCond:        rc,
		serverReadDone:  0,
		clientWaitgroup: &wg,
		serverWaitCond:  sd,
	}

	return pfc
}

// AddClient adds an io.Writer client to the Proxy Forward Collapser
// This client will read all the cached data and read from the live edge if caught up.
func (pfc *proxyForwardCollapser) AddClient(w io.Writer) {
	pfc.clientWaitgroup.Add(1)
	readAndRespondFromPFC(pfc.Read, w)
	pfc.clientWaitgroup.Done()
}

// WaitServerComplete blocks until the object has been retreived from the origin server
// Need to get payload before can send to actual cache
func (pfc *proxyForwardCollapser) WaitServerComplete() {
	pfc.serverWaitCond.Wait()
	return
}

// WaitAllComplete will wait till all clients have completed or timedout
// Need to no abandon goroutines
func (pfc *proxyForwardCollapser) WaitAllComplete() {
	pfc.clientWaitgroup.Wait()
	return
}

func (pfc *proxyForwardCollapser) GetBody(n uint64) []byte {
	if n > uint64(len(pfc.dataStore)) {
		return nil
	}
	return pfc.dataStore[:n]
}

func (pfc *proxyForwardCollapser) GetResp() *http.Response {
	return pfc.resp
}

// Write writes the data in b to the Proxy Forward Collapsers data store,
// adds a reference to that data, and increments the read index.
func (pfc *proxyForwardCollapser) Write(b []byte) (int, error) {
	n := atomic.LoadUint64(&pfc.rIndex)
	pfc.data[n] = pfc.dataStore[pfc.dataIndex : pfc.dataIndex+uint64(len(b))]
	copy(pfc.data[n], b)
	atomic.AddUint64(&pfc.rIndex, 1)
	pfc.readCond.Broadcast()
	return len(b), nil
}

// Close signals all things waiting on the server response body to complete.
// This should be triggered by the client io.EOF
func (pfc *proxyForwardCollapser) Close() {
	atomic.AddInt32(&pfc.serverReadDone, 1)
	pfc.serverWaitCond.Signal()
	pfc.readCond.Broadcast()
	return
}

// Read will return the given index data requested by the read is behind the PFC readindex, else blocks and waits for the data
func (pfc *proxyForwardCollapser) Read(index uint64, b []byte) (int, error) {
	i := atomic.LoadUint64(&pfc.rIndex)
	if index >= i {
		// need to check completion and return io.EOF
		if atomic.LoadInt32(&pfc.serverReadDone) != 0 {
			return 0, io.EOF
		}
		pfc.readCond.L.Lock()
		pfc.readCond.Wait()
		pfc.readCond.L.Unlock()
	}
	copy(b, pfc.data[index])
	return len(pfc.data[index]), nil
}
