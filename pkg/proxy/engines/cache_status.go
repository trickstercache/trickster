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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// cacheStatusID names this cache in the Cache-Status field. RFC 9211 wants the
// first member to identify the cache that added the rest of them.
const cacheStatusID = "trickster"

// RFC 9211 2.2.2 forward reasons
const (
	fwdBypass  = "bypass"
	fwdMiss    = "uri-miss"
	fwdPartial = "partial"
	fwdRequest = "request"
	fwdStale   = "stale"
	fwdVary    = "vary-miss"
)

// addBypassCacheStatus reports a hop that did not consult storage at all,
// which is what a proxy-only path and a non-caching backend both are.
func addBypassCacheStatus(h http.Header, httpStatus int) {
	if h == nil {
		return
	}
	h.Add(headers.NameCacheStatus,
		cacheStatusID+"; fwd="+fwdBypass+"; fwd-status="+strconv.Itoa(httpStatus))
}

// setCacheStatusHeader describes how this cache handled the request, per
// RFC 9211. Members run in the order the RFC presents them: what the cache
// did, then what it learned from the next hop, then what it kept.
func (pr *proxyRequest) setCacheStatusHeader() {
	if pr.upstreamResponse == nil || pr.upstreamResponse.Header == nil {
		return
	}
	var sb strings.Builder
	sb.WriteString(cacheStatusID)

	switch pr.cacheStatus {
	case status.LookupStatusHit, status.LookupStatusNegativeCacheHit:
		sb.WriteString("; hit")
		pr.appendTTL(&sb)
	case status.LookupStatusProxyHit:
		// this client joined a fetch another one had already started
		sb.WriteString("; hit; collapsed")
	case status.LookupStatusRevalidated:
		// the stored response was reused, but only after the origin confirmed it
		sb.WriteString("; fwd=" + fwdStale + "; fwd-status=304")
		pr.appendTTL(&sb)
	default:
		sb.WriteString("; fwd=" + pr.forwardReason())
		sb.WriteString("; fwd-status=" + strconv.Itoa(pr.upstreamResponse.StatusCode))
		if pr.writeToCache {
			sb.WriteString("; stored")
		}
	}
	pr.upstreamResponse.Header.Add(headers.NameCacheStatus, sb.String())
}

// forwardReason names why the cache had to contact the origin.
func (pr *proxyRequest) forwardReason() string {
	switch {
	case pr.cachingPolicy != nil && pr.cachingPolicy.ClientDirectives != nil &&
		(pr.cachingPolicy.ClientDirectives.NoCache ||
			pr.cachingPolicy.ClientDirectives.OnlyIfCached):
		return fwdRequest
	case pr.cacheStatus == status.LookupStatusPartialHit,
		pr.cacheStatus == status.LookupStatusRangeMiss:
		return fwdPartial
	case pr.cacheDocument != nil:
		// something was stored for this URI, it just could not be reused
		return fwdStale
	case len(pr.varyNames) > 0:
		// the URI was known, but no stored variant matched this request
		return fwdVary
	}
	return fwdMiss
}

// appendTTL reports the stored response's remaining freshness lifetime. RFC
// 9211 2.4 allows it to be negative, which says the response was served while
// stale rather than that the cache miscounted.
func (pr *proxyRequest) appendTTL(sb *strings.Builder) {
	cp := pr.cachingPolicy
	if cp == nil {
		return
	}
	sb.WriteString("; ttl=" + strconv.Itoa(cp.FreshnessLifetime-cp.CurrentAge(time.Now())))
}

// setAgeHeader states how old a response served from storage is. RFC 9111 5.1
// requires a cache to generate the field whenever it reuses what it holds; a
// response fetched just now carries whatever age the origin gave it instead.
func (pr *proxyRequest) setAgeHeader() {
	if pr.upstreamResponse == nil || pr.upstreamResponse.Header == nil {
		return
	}
	switch pr.cacheStatus {
	case status.LookupStatusHit, status.LookupStatusPartialHit,
		status.LookupStatusRevalidated, status.LookupStatusNegativeCacheHit:
	default:
		return
	}
	pr.upstreamResponse.Header.Set(headers.NameAge,
		strconv.Itoa(pr.cachingPolicy.CurrentAge(time.Now())))
}
