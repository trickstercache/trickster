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
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

//go:generate msgp

// CachingPolicy defines the attributes for determining the cachability of an HTTP object
type CachingPolicy struct {
	IsFresh              bool `msg:"is_fresh"`
	NoCache              bool `msg:"nocache"`
	NoTransform          bool `msg:"notransform"`
	CanRevalidate        bool `msg:"can_revalidate"`
	MustRevalidate       bool `msg:"must_revalidate"`
	IsNegativeCache      bool `msg:"is_negative_cache"`
	IsClientConditional  bool `msg:"-"`
	IsClientFresh        bool `msg:"-"`
	HasIfModifiedSince   bool `msg:"-"`
	HasIfUnmodifiedSince bool `msg:"-"`
	HasIfNoneMatch       bool `msg:"-"`
	IfNoneMatchResult    bool `msg:"-"`

	FreshnessLifetime int `msg:"freshness_lifetime"`

	LastModified time.Time `msg:"last_modified"`
	Expires      time.Time `msg:"expires"`
	Date         time.Time `msg:"date"`
	LocalDate    time.Time `msg:"local_date"`

	ETag string `msg:"etag"`

	IfNoneMatchValue      string    `msg:"-"`
	IfModifiedSinceTime   time.Time `msg:"-"`
	IfUnmodifiedSinceTime time.Time `msg:"-"`
}

// Clone returns an exact copy of the Caching Policy
func (cp *CachingPolicy) Clone() *CachingPolicy {
	return &CachingPolicy{
		IsFresh:               cp.IsFresh,
		NoCache:               cp.NoCache,
		NoTransform:           cp.NoTransform,
		FreshnessLifetime:     cp.FreshnessLifetime,
		CanRevalidate:         cp.CanRevalidate,
		MustRevalidate:        cp.MustRevalidate,
		LastModified:          cp.LastModified,
		Expires:               cp.Expires,
		Date:                  cp.Date,
		LocalDate:             cp.LocalDate,
		ETag:                  cp.ETag,
		IsNegativeCache:       cp.IsNegativeCache,
		IfNoneMatchValue:      cp.IfNoneMatchValue,
		IfModifiedSinceTime:   cp.IfModifiedSinceTime,
		IfUnmodifiedSinceTime: cp.IfUnmodifiedSinceTime,
		IsClientConditional:   cp.IsClientConditional,
		IsClientFresh:         cp.IsClientFresh,
		HasIfModifiedSince:    cp.HasIfModifiedSince,
		HasIfUnmodifiedSince:  cp.HasIfUnmodifiedSince,
		HasIfNoneMatch:        cp.HasIfNoneMatch,
		IfNoneMatchResult:     cp.IfNoneMatchResult,
	}
}

// ResetClientConditionals sets the request-specific conditional values of the subject
// caching policy to false, so as to facilitate reuse of the policy with subsequent requests
// for the same cache object
func (cp *CachingPolicy) ResetClientConditionals() {
	cp.IfNoneMatchValue = ""
	cp.IfModifiedSinceTime = time.Time{}
	cp.IfUnmodifiedSinceTime = time.Time{}
	cp.IsClientConditional = false
	cp.IsClientFresh = false
	cp.HasIfModifiedSince = false
	cp.HasIfUnmodifiedSince = false
	cp.HasIfNoneMatch = false
	cp.IfNoneMatchResult = false
}

// Merge merges the source CachingPolicy into the subject CachingPolicy
func (cp *CachingPolicy) Merge(src *CachingPolicy) {

	if src == nil {
		return
	}

	cp.NoCache = cp.NoCache || src.NoCache
	cp.NoTransform = cp.NoTransform || src.NoTransform

	cp.IsClientConditional = cp.IsClientConditional || src.IsClientConditional
	cp.IsClientFresh = cp.IsClientFresh || src.IsClientFresh
	cp.IsNegativeCache = cp.IsNegativeCache || src.IsNegativeCache

	cp.IsFresh = src.IsFresh
	cp.FreshnessLifetime = src.FreshnessLifetime
	cp.CanRevalidate = src.CanRevalidate
	cp.MustRevalidate = src.MustRevalidate
	cp.LastModified = src.LastModified
	cp.Expires = src.Expires
	cp.Date = src.Date
	cp.LocalDate = src.LocalDate
	cp.ETag = src.ETag

	// request policies (e.g., IfModifiedSince) are intentionally omitted,
	// assuming a response policy is always merged into a request policy

}

// TTL returns a TTL based on the subject caching policy and the provided multiplier and max values
func (cp *CachingPolicy) TTL(multiplier float64, max time.Duration) time.Duration {
	var ttl time.Duration = time.Duration(cp.FreshnessLifetime) * time.Second
	if cp.CanRevalidate {
		ttl *= time.Duration(multiplier)
	}
	if ttl > max {
		ttl = max
	}
	return ttl
}

func (cp *CachingPolicy) String() string {
	return fmt.Sprintf(`{ "is_fresh":%t, "no_cache":%t, "no_transform":%t, 
	"freshness_lifetime":%d, "can_revalidate":%t, "must_revalidate":%t,`+
		` "last_modified":%d, "expires":%d, "date":%d, "local_date":%d, "etag":"%s", "if_none_match":"%s"`+
		` "if_modified_since":%d, "if_unmodified_since":%d, "is_negative_cache":%t }`,
		cp.IsFresh, cp.NoCache, cp.NoTransform, cp.FreshnessLifetime, cp.CanRevalidate, cp.MustRevalidate,
		cp.LastModified.Unix(), cp.Expires.Unix(), cp.Date.Unix(), cp.LocalDate.Unix(), cp.ETag,
		cp.IfNoneMatchValue, cp.IfModifiedSinceTime.Unix(), cp.IfUnmodifiedSinceTime.Unix(), cp.IsNegativeCache)
}

// GetResponseCachingPolicy examines HTTP response headers for caching headers
// a returns a CachingPolicy reference
func GetResponseCachingPolicy(code int, negativeCache map[int]time.Duration, h http.Header) *CachingPolicy {

	cp := &CachingPolicy{LocalDate: time.Now()}

	if d, ok := negativeCache[code]; ok {
		cp.FreshnessLifetime = int(d.Seconds())
		cp.Expires = cp.LocalDate.Add(d)
		cp.IsNegativeCache = true
		return cp
	}

	// Do not cache content that includes set-cookie header
	// Trickster can use PathConfig rules to strip set-cookie if cachablility is needed
	if v := h.Get(headers.NameSetCookie); v != "" {
		cp.NoCache = true
		cp.FreshnessLifetime = -1
		return cp
	}

	// Cache-Control has first precedence
	if v := h.Get(headers.NameCacheControl); v != "" {
		cp.parseCacheControlDirectives(v)
	}

	if cp.NoCache {
		cp.FreshnessLifetime = -1
		return cp
	}

	lastModifiedHeader := h.Get(headers.NameLastModified)
	hasLastModified := lastModifiedHeader != ""
	expiresHeader := h.Get(headers.NameExpires)
	hasExpires := expiresHeader != ""
	eTagHeader := h.Get(headers.NameETag)
	hasETag := eTagHeader != ""

	if !hasLastModified && !hasExpires && !hasETag && cp.FreshnessLifetime == 0 {
		cp.NoCache = true
		cp.FreshnessLifetime = -1
		return cp
	}

	// Get the date header or, if it is not found or parsed, set it
	if v := h.Get(headers.NameDate); v != "" {
		if date, err := time.Parse(time.RFC1123, v); err != nil {
			cp.Date = cp.LocalDate
			h.Set(headers.NameDate, cp.Date.UTC().Format(time.RFC1123))
		} else {
			cp.Date = date
		}
	} else {
		cp.Date = cp.LocalDate
		h.Set(headers.NameDate, cp.Date.UTC().Format(time.RFC1123))
	}

	// no Max-Age provided yet, look for expires
	if cp.FreshnessLifetime == 0 && !cp.MustRevalidate {
		// if there is an Expires header, respect it
		if hasExpires {
			expires, err := time.Parse(time.RFC1123, expiresHeader)
			if err == nil {
				cp.Expires = expires
				if expires.Before(cp.Date) {
					cp.FreshnessLifetime = -1
					cp.MustRevalidate = true
				} else {
					cp.FreshnessLifetime = int(cp.Expires.Sub(cp.Date).Seconds())
				}
			} else {
				cp.FreshnessLifetime = -1
				cp.MustRevalidate = true
			}
		}
	}

	if !hasETag && !hasLastModified {
		cp.CanRevalidate = false
		return cp
	}

	cp.CanRevalidate = true

	if hasETag {
		cp.ETag = eTagHeader
	}

	if hasLastModified {
		lm, err := time.Parse(time.RFC1123, lastModifiedHeader)
		if err != nil {
			cp.CanRevalidate = false
			cp.FreshnessLifetime = -1
		} else {
			cp.LastModified = lm
		}
	}

	// else, if there is a Last-Modified header, set FreshnessLifetime to 20% of age
	if cp.CanRevalidate && cp.FreshnessLifetime == 0 && !cp.LastModified.IsZero() &&
		cp.LastModified.Before(cp.Date) && !cp.MustRevalidate {
		objectAge := int(cp.Date.Sub(cp.LastModified).Seconds())
		if objectAge > 0 {
			cp.FreshnessLifetime = objectAge / 5
		}
	}

	return cp
}

var supportedCCD = map[string]bool{
	headers.ValuePrivate:         true,
	headers.ValueNoCache:         true,
	headers.ValueNoStore:         true,
	headers.ValueMaxAge:          false,
	headers.ValueSharedMaxAge:    false,
	headers.ValueMustRevalidate:  false,
	headers.ValueProxyRevalidate: false,
}

func (cp *CachingPolicy) parseCacheControlDirectives(directives string) {
	dl := strings.Split(strings.Replace(strings.ToLower(directives), " ", "", -1), ",")
	var noCache bool
	var hasSharedMaxAge bool
	var foundFreshnessDirective bool
	for _, d := range dl {
		var dsub string
		if i := strings.Index(d, "="); i > 0 {
			dsub = d[i+1:]
			d = d[:i]
		}
		if v, ok := supportedCCD[d]; ok {
			noCache = noCache || v
		}
		if noCache {
			cp.NoCache = true
			cp.FreshnessLifetime = -1
			return
		}
		if d == headers.ValueSharedMaxAge && dsub != "" {
			foundFreshnessDirective = true
			secs, err := strconv.Atoi(dsub)
			if err == nil {
				hasSharedMaxAge = true
				cp.FreshnessLifetime = secs
			}
		}
		if (!hasSharedMaxAge) && d == headers.ValueMaxAge && dsub != "" {
			foundFreshnessDirective = true
			secs, err := strconv.Atoi(dsub)
			if err == nil {
				cp.FreshnessLifetime = secs
			}
		}
		if (d == headers.ValueMustRevalidate || d == headers.ValueProxyRevalidate) ||
			(cp.FreshnessLifetime == 0 && foundFreshnessDirective) {
			cp.MustRevalidate = true
			cp.FreshnessLifetime = 0
		}
		if d == headers.ValueNoTransform {
			cp.NoTransform = true
		}
	}

}

func hasPragmaNoCache(h http.Header) bool {
	if v := h.Get(headers.NamePragma); v != "" {
		return v == headers.ValueNoCache
	}
	return false
}

// GetRequestCachingPolicy examines HTTP request headers for caching headers
// and true if the corresponding response is OK to cache
func GetRequestCachingPolicy(h http.Header) *CachingPolicy {
	cp := &CachingPolicy{LocalDate: time.Now()}

	if hasPragmaNoCache(h) {
		cp.NoCache = true
		return cp
	}

	// Cache-Control has first precedence
	if v := h.Get(headers.NameCacheControl); v != "" {
		cp.parseCacheControlDirectives(v)
		if cp.NoCache {
			return cp
		}
	}

	if v := h.Get(headers.NameIfModifiedSince); v != "" {
		if date, err := time.Parse(time.RFC1123, v); err == nil {
			cp.IfModifiedSinceTime = date
		}
	}

	if v := h.Get(headers.NameIfUnmodifiedSince); v != "" {
		if date, err := time.Parse(time.RFC1123, v); err == nil {
			cp.IfUnmodifiedSinceTime = date
		}
	}

	if v := h.Get(headers.NameIfNoneMatch); v != "" {
		cp.IfNoneMatchValue = v
	}

	return cp
}

// ResolveClientConditionals ensures any client conditionals are handled before
// responding to the client request
func (cp *CachingPolicy) ResolveClientConditionals(ls status.LookupStatus) {

	cp.IsClientFresh = false
	if !cp.IsClientConditional {
		return
	}

	isClientFresh := true
	if cp.HasIfNoneMatch {
		cp.IfNoneMatchResult = CheckIfNoneMatch(cp.ETag, cp.IfNoneMatchValue, ls)
		isClientFresh = isClientFresh && !cp.IfNoneMatchResult
	}
	if cp.HasIfModifiedSince {
		isClientFresh = isClientFresh && !cp.LastModified.After(cp.IfModifiedSinceTime)
	}
	if cp.HasIfUnmodifiedSince {
		isClientFresh = isClientFresh && cp.LastModified.After(cp.IfUnmodifiedSinceTime)
	}
	cp.IsClientFresh = isClientFresh
}

// ParseClientConditionals inspects the client http request to determine if it includes any conditions
func (cp *CachingPolicy) ParseClientConditionals() {
	cp.HasIfNoneMatch = cp.IfNoneMatchValue != ""
	cp.HasIfModifiedSince = !cp.IfModifiedSinceTime.IsZero()
	cp.HasIfUnmodifiedSince = !cp.IfUnmodifiedSinceTime.IsZero()
	cp.IsClientConditional = cp.HasIfNoneMatch || cp.HasIfModifiedSince || cp.HasIfUnmodifiedSince
}

// CheckIfNoneMatch determines if the provided match value satisfies an "If-None-Match"
// condition against the cached object. As Trickster is a cache, matching is always weak.
func CheckIfNoneMatch(etag string, headerValue string, ls status.LookupStatus) bool {

	if etag == "" || headerValue == "" {
		return etag == headerValue
	}

	if headerValue == "*" {
		if ls == status.LookupStatusHit || ls == status.LookupStatusRevalidated {
			return false
		}
		return true
	}

	parts := strings.Split(headerValue, ",")
	for _, p := range parts {
		p = strings.Trim(p, " ")
		if len(p) > 3 && p[1:2] == "/" {
			p = p[2:]
		}
		if len(p) > 1 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
			p = p[1 : len(p)-1]
		}
		if p == etag {
			return false
		}
	}

	return true
}
