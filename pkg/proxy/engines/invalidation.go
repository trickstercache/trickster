/*
 * Copyright 2018 The Trickster Authors
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
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// ProxyAndInvalidate proxies a request the cache does not serve and drops any
// stored response for the target URI as soon as the origin accepts the change.
//
// RFC 9111 4.4 ties invalidation to receiving the non-error response, not to
// finishing it. Waiting for the body to finish copying would leave the
// superseded representation readable for the length of the transfer, so a slow
// write could be overtaken by a read it had already invalidated.
func ProxyAndInvalidate(w http.ResponseWriter, r *http.Request) {
	reader, resp, _ := PrepareFetchReader(r)
	if resp == nil {
		return
	}
	if resp.StatusCode < http.StatusBadRequest && methods.IsStateChanging(r.Method) {
		InvalidateTargetURI(r)
	}
	if reader != nil {
		defer reader.Close()
	}
	setStatusHeader(resp.StatusCode, resp.Header)
	writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header, nil)
	if writer == nil || reader == nil {
		return
	}
	if _, err := io.Copy(streamWriter(writer, resp), reader); err != nil {
		logger.Error("proxy response copy failed",
			logging.Pairs{keys.Error: err.Error()})
		abortOnCopyError(writer, r, err)
	}
}

// InvalidateTargetURI removes the cache entries that a later GET or HEAD for
// this request's target URI would read. Each cacheable method has its own
// primary key, and an authenticated write also has to reach the credential-free
// key a shareable response was stored under, so every combination is dropped.
//
// Dropping a primary key retires the whole URI even when it held only a variant
// index: the index scopes its variants by generation, so variants that outlive
// it can never be selected again.
func InvalidateTargetURI(r *http.Request) {
	if r == nil {
		return
	}
	rsc := request.GetResources(r)
	if rsc == nil || rsc.CacheClient == nil || rsc.BackendOptions == nil {
		return
	}
	o := rsc.BackendOptions
	authed := r.Header.Get(headers.NameAuthorization) != ""
	for _, m := range methods.CacheableHTTPMethods() {
		cr := r.Clone(r.Context())
		cr.Method = m
		// the write's body is neither part of a read's cache key nor still
		// readable here, and dropping it keeps DeriveCacheKey off the body path
		cr.Body = nil
		cr.GetBody = nil
		pr := &proxyRequest{Request: cr, rsc: rsc, upstreamRequest: cr}
		rsc.CacheClient.Remove(ComposeCacheKey(o.Name, o.CacheKeyPrefix, "opc",
			pr.DeriveCacheKey("")))
		if !authed {
			continue
		}
		// a response the origin marked shareable was stored without the
		// credential, and this write supersedes that copy too
		pr.omitAuthFromKey = true
		rsc.CacheClient.Remove(ComposeCacheKey(o.Name, o.CacheKeyPrefix, "opc",
			pr.DeriveCacheKey("")))
	}
}
