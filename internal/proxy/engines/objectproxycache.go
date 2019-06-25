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
	"strconv"
	"time"

	tc "github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/pkg/locks"
)

// ObjectProxyCacheRequest provides a Basic HTTP Reverse Proxy/Cache
func ObjectProxyCacheRequest(r *model.Request, w http.ResponseWriter, client model.Client, cache tc.Cache, ttl time.Duration, refresh bool, noLock bool) {
	body, resp, _ := FetchViaObjectProxyCache(r, client, cache, ttl, refresh, noLock)
	Respond(w, resp.StatusCode, resp.Header, body)
}

// FetchViaObjectProxyCache Fetches an object from Cache or Origin (on miss), writes the object to the cache, and returns the object to the caller
func FetchViaObjectProxyCache(r *model.Request, client model.Client, cache tc.Cache, ttl time.Duration, refresh bool, noLock bool) ([]byte, *http.Response, bool) {

	key := client.Configuration().Host + "." + client.DeriveCacheKey(r, r.Headers.Get(headers.NameAuthorization))
	if !noLock {
		locks.Acquire(key)
		defer locks.Release(key)
	}

	if !refresh {
		if d, err := QueryCache(cache, key); err == nil {
			recordOPCResult(r, tc.LookupStatusHit, "200", r.URL.Path, 0, d.Headers)
			log.Debug("cache hit", log.Pairs{"key": key})
			rsp := &http.Response{
				Header:     d.Headers,
				StatusCode: d.StatusCode,
				Status:     d.Status,
			}
			return d.Body, rsp, true
		}
	}

	body, resp, elapsed := Fetch(r)
	recordOPCResult(r, tc.LookupStatusKeyMiss, strconv.Itoa(resp.StatusCode), r.URL.Path, elapsed.Seconds(), resp.Header)

	if resp.StatusCode == http.StatusOK && len(body) > 0 {
		WriteCache(cache, key, model.DocumentFromHTTPResponse(resp, body), ttl)
	}

	return body, resp, false

}

func recordOPCResult(r *model.Request, cacheStatus tc.LookupStatus, httpStatus, path string, elapsed float64, header http.Header) {
	recordResults(r, "ObjectProxyCache", cacheStatus.String(), httpStatus, path, "", elapsed, nil, header)
}
