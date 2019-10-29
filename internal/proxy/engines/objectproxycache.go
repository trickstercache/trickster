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

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, noLock bool) {
	body, resp, _ := FetchViaObjectProxyCache(r, client, nil, noLock)
	Respond(w, resp.StatusCode, resp.Header, body)
}

// FetchViaObjectProxyCache Fetches an object from Cache or Origin (on miss), writes the object to the cache, and returns the object to the caller
func FetchViaObjectProxyCache(r *model.Request, client model.Client, apc *config.PathConfig, noLock bool) ([]byte, *http.Response, bool) {

	oc := context.OriginConfig(r.ClientRequest.Context())
	cache := context.CacheClient(r.ClientRequest.Context())

	key := oc.Host + "." + DeriveCacheKey(client, r, apc, "")

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

func recordOPCResult(r *model.Request, cacheStatus tc.LookupStatus, httpStatus int, path string, elapsed float64, header http.Header) {
	recordResults(r, "ObjectProxyCache", cacheStatus, httpStatus, path, "", elapsed, nil, header)
}

// WIP

// Need maxfilesize option - if 2-gb file then just proxy it
// Want proxy or proxy with RFC
// The OBC with RFC

/*

TODO:
Add config options
Clean up the implementation on the httpproxy
Implement this on the object proxy cache
Write unit tests
Write documentation




*/

type indexReader func(index uint64, b []byte) (int, error)

func readAndRespondFromPFC(ir indexReader, w io.Writer) {
	var readIndex uint64
	var err error
	buf := make([]byte, HTTPBlockSize)

	for {
		_, err = ir(readIndex, buf)
		if err != nil {
			if err == io.EOF {
				fmt.Println("readResp EOF")
			}
			break
		}
		w.Write(buf)
		readIndex++
	}
}

type ProxyForwardCollapser interface {
	AddClient(io.Writer)
	Write([]byte) (int, error)
	Close()
	Read(uint64, []byte) (int, error)
	WaitServerComplete()
	WaitAllComplete()
}

type proxyForwardCollapser struct {
	rIndex          uint64
	dataLength      uint64
	dataIndex       uint64
	data            [][]byte
	dataStore       []byte
	readCond        *sync.Cond
	serverReadDone  int32
	clientWaitgroup *sync.WaitGroup
	serverWaitCond  *sync.Cond
}

func NewPFC(clientTimeout time.Duration, blockCount int) ProxyForwardCollapser {
	dataStore := make([]byte, blockCount*HTTPBlockSize)
	refs := make([][]byte, blockCount*2)

	var wg sync.WaitGroup
	sd := sync.NewCond(&sync.Mutex{})
	rc := sync.NewCond(&sync.Mutex{})

	// Create goroutine for client
	// This goroutine should try to read everything that is available from the PFC byte store
	pfc := &proxyForwardCollapser{
		rIndex:          0,
		dataLength:      uint64(blockCount),
		data:            refs,
		dataStore:       dataStore,
		readCond:        rc,
		serverReadDone:  0,
		clientWaitgroup: &wg,
		serverWaitCond:  sd,
	}

	return pfc
}

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

// RundownCollapsedForwarding
// ConcurrentCollapsedForwarding
// ProgressiveCollapsedForwarding
