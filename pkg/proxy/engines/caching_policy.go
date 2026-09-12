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
	"github.com/trickstercache/trickster/v2/pkg/proxy/cachecontrol"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

//go:generate go tool msgp

// CachingPolicy defines the attributes for determining the cachability of an HTTP object
type CachingPolicy struct {
	IsFresh         bool `msg:"is_fresh"`
	NoCache         bool `msg:"nocache"`
	NoTransform     bool `msg:"notransform"`
	CanRevalidate   bool `msg:"can_revalidate"`
	MustRevalidate  bool `msg:"must_revalidate"`
	IsNegativeCache bool `msg:"is_negative_cache"`
	// IsShareable records that the origin marked the response reusable by a
	// shared cache, which RFC 9111 3.5 requires before one may store a
	// response to a request that carried Authorization
	IsShareable          bool `msg:"-"`
	IsClientConditional  bool `msg:"-"`
	IsClientFresh        bool `msg:"-"`
	HasIfModifiedSince   bool `msg:"-"`
	HasIfUnmodifiedSince bool `msg:"-"`
	HasIfNoneMatch       bool `msg:"-"`
	IfNoneMatchResult    bool `msg:"-"`
	HasIfRange           bool `msg:"-"`

	FreshnessLifetime int `msg:"freshness_lifetime"`
	// NoStaleServing records an in-protocol prohibition on reusing this
	// response once stale. RFC 9111 4.2.4 bars must-revalidate,
	// proxy-revalidate and an applicable s-maxage from being served past their
	// lifetime without successful validation.
	NoStaleServing bool `msg:"no_stale_serving"`
	// StaleWhileRevalidate and StaleIfError are the RFC 5861 windows, in
	// seconds past the freshness lifetime, during which this response may
	// still be served: the first while a refresh runs, the second when the
	// origin is failing.
	StaleWhileRevalidate int `msg:"stale_while_revalidate"`
	StaleIfError         int `msg:"stale_if_error"`

	// InitialAge is how old the response already was when it was stored, in
	// seconds: RFC 9111 4.2.3 calls this corrected_initial_age. A response
	// that aged in an upstream cache spends that much of its lifetime before
	// this cache ever sees it.
	InitialAge int `msg:"initial_age"`

	LastModified time.Time `msg:"last_modified"`
	Expires      time.Time `msg:"expires"`
	Date         time.Time `msg:"date"`
	LocalDate    time.Time `msg:"local_date"`

	ETag string `msg:"etag"`

	IfNoneMatchValue      string    `msg:"-"`
	IfModifiedSinceTime   time.Time `msg:"-"`
	IfUnmodifiedSinceTime time.Time `msg:"-"`
	IfRangeValue          string    `msg:"-"`

	// ClientDirectives are the request's own Cache-Control directives. They
	// belong to one request rather than to the stored object, so they are
	// never serialized and never merged in from a response.
	ClientDirectives *cachecontrol.RequestDirectives `msg:"-"`
}

// Clone returns an exact copy of the Caching Policy
func (cp *CachingPolicy) Clone() *CachingPolicy {
	return pointers.Clone(cp)
}

// ResetClientConditionals sets the request-specific conditional values of the subject
// caching policy to false, so as to facilitate reuse of the policy with subsequent requests
// for the same cache object
func (cp *CachingPolicy) ResetClientConditionals() {
	cp.IfNoneMatchValue = ""
	cp.IfRangeValue = ""
	cp.HasIfRange = false
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

	// permission to share is a property of the response in hand. OR-ing it
	// would let a replacement inherit the public marking of the representation
	// it supersedes, and be stored where anyone could read it.
	cp.IsShareable = src.IsShareable
	cp.NoStaleServing = src.NoStaleServing

	cp.IsFresh = src.IsFresh
	cp.FreshnessLifetime = src.FreshnessLifetime
	cp.StaleWhileRevalidate = src.StaleWhileRevalidate
	cp.StaleIfError = src.StaleIfError
	cp.CanRevalidate = src.CanRevalidate
	cp.MustRevalidate = src.MustRevalidate
	cp.LastModified = src.LastModified
	cp.Expires = src.Expires
	cp.Date = src.Date
	cp.LocalDate = src.LocalDate
	cp.InitialAge = src.InitialAge
	cp.ETag = src.ETag

	// the destination is the request's policy and the source describes a
	// response, so everything the client asked for -- its conditionals and its
	// Cache-Control directives -- is left as it was
}

// minRevalidationWindow is the least time past its freshness that a
// revalidatable object is kept. A multiplier alone leaves a short-lived object
// almost no window -- two seconds for a one-second lifetime -- discarding the
// validator exactly when it would start saving a body transfer.
const minRevalidationWindow = time.Minute

// TTL returns how long to retain the object. Retention outlives freshness: a
// cache needs the stored response to revalidate cheaply, to answer with while
// a refresh runs, and to fall back on when the origin is failing.
func (cp *CachingPolicy) TTL(multiplier float64, maxDur time.Duration) time.Duration {
	fresh := time.Duration(cp.FreshnessLifetime) * time.Second
	ttl := fresh
	if cp.CanRevalidate {
		ttl = max(time.Duration(float64(fresh)*multiplier), fresh+minRevalidationWindow)
	}
	if w := max(cp.StaleWhileRevalidate, cp.StaleIfError); w > 0 {
		ttl = max(ttl, fresh+time.Duration(w)*time.Second)
	}
	if ttl > maxDur {
		ttl = maxDur
	}
	return ttl
}

// staleFor returns how many seconds past its freshness lifetime the response
// is, or a negative number while it is still fresh.
func (cp *CachingPolicy) staleFor(now time.Time) int {
	return cp.CurrentAge(now) - cp.FreshnessLifetime
}

// CanServeStaleWhileRevalidate reports whether the response may be served as
// it is while a refresh runs behind it (RFC 5861 3).
func (cp *CachingPolicy) CanServeStaleWhileRevalidate(now time.Time) bool {
	if cp == nil || cp.StaleWhileRevalidate <= 0 || cp.NoStaleServing {
		return false
	}
	s := cp.staleFor(now)
	return s >= 0 && s <= cp.StaleWhileRevalidate
}

// CanServeStaleOnError reports whether the response may stand in for an origin
// that is failing (RFC 5861 4).
func (cp *CachingPolicy) CanServeStaleOnError(now time.Time) bool {
	if cp == nil || cp.StaleIfError <= 0 || cp.NoStaleServing {
		return false
	}
	s := cp.staleFor(now)
	return s >= 0 && s <= cp.StaleIfError
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

	// Cache-Control has first precedence, except where RFC 9213 gives a
	// targeted field precedence over it
	cp.applyResponseDirectives(
		cachecontrol.ParseResponseTargeted(h, headers.NameCDNCacheControl))

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

	// RFC 9111 4.2.3: the response may already have aged in an upstream cache,
	// and whichever is larger -- the age it reports or the gap between its Date
	// and now -- counts against the lifetime it has left here
	apparentAge := int(cp.LocalDate.Sub(cp.Date).Seconds())
	cp.InitialAge = max(apparentAge, upstreamAge(h), 0)

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

// applyResponseDirectives maps parsed response directives onto the policy.
// Trickster is a shared cache, so private is as disqualifying as no-store.
func (cp *CachingPolicy) applyResponseDirectives(d *cachecontrol.ResponseDirectives) {
	if d == nil {
		return
	}
	cp.NoTransform = d.NoTransform
	cp.IsShareable = d.Public || d.SharedMaxAge != nil || d.MustRevalidate
	if d.NoCache || d.NoStore || d.Private {
		cp.NoCache = true
		cp.FreshnessLifetime = -1
		return
	}
	if secs, ok := d.Lifetime(); ok {
		cp.FreshnessLifetime = secs
	}
	if d.StaleWhileRevalidate != nil {
		cp.StaleWhileRevalidate = *d.StaleWhileRevalidate
	}
	if d.StaleIfError != nil {
		cp.StaleIfError = *d.StaleIfError
	}
	// a stated lifetime of zero, or one whose argument would not parse, leaves
	// nothing to serve without checking with the origin first
	// s-maxage carries the force of proxy-revalidate for a shared cache, so
	// each of these rules out serving the response stale. The freshness
	// lifetime itself is left alone: it still says when the response stops
	// being servable without validation.
	cp.NoStaleServing = d.MustRevalidate || d.ProxyRevalidate || d.SharedMaxAge != nil

	if d.MustRevalidate || d.ProxyRevalidate ||
		(d.FreshnessSeen && cp.FreshnessLifetime == 0) {
		cp.MustRevalidate = true
		cp.FreshnessLifetime = 0
	}
}

// clientAcceptsStored reports whether the client's own Cache-Control leaves
// this stored response usable. RFC 9111 5.2.1 lets a request narrow what
// counts as fresh enough for it, so a response the cache still considers fresh
// may have to be checked with the origin anyway.
func (cp *CachingPolicy) clientAcceptsStored(now time.Time) bool {
	d := cp.ClientDirectives
	if d == nil {
		return true
	}
	age := cp.CurrentAge(now)
	if d.MaxAge != nil && age > *d.MaxAge {
		return false
	}
	if d.MinFresh != nil && cp.FreshnessLifetime-age < *d.MinFresh {
		return false
	}
	return true
}

// OnlyIfCached reports that the client refuses a forwarded response.
func (cp *CachingPolicy) OnlyIfCached() bool {
	return cp != nil && cp.ClientDirectives != nil && cp.ClientDirectives.OnlyIfCached
}

// CurrentAge returns how many seconds old the stored response is now: the age
// it arrived with plus the time it has been resident here (RFC 9111 4.2.3).
func (cp *CachingPolicy) CurrentAge(now time.Time) int {
	if cp == nil {
		return 0
	}
	return max(cp.InitialAge+int(now.Sub(cp.LocalDate).Seconds()), 0)
}

// upstreamAge reads the Age field an upstream cache attached, in seconds. An
// unparsable or negative value is treated as absent, per RFC 9111 4.2.3.
func upstreamAge(h http.Header) int {
	v := h.Get(headers.NameAge)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
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

	cp.ClientDirectives = cachecontrol.ParseRequest(h)
	cp.NoTransform = cp.ClientDirectives.NoTransform

	// a request that will not accept a stored response bypasses the cache
	// outright; nothing else it asks for changes that
	if hasPragmaNoCache(h) || cp.ClientDirectives.NoCache || cp.ClientDirectives.NoStore {
		cp.NoCache = true
		return cp
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

	if v := h.Get(headers.NameIfRange); v != "" {
		cp.IfRangeValue = v
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
	cp.HasIfRange = cp.IfRangeValue != ""
	cp.HasIfNoneMatch = cp.IfNoneMatchValue != ""
	cp.HasIfModifiedSince = !cp.IfModifiedSinceTime.IsZero()
	cp.HasIfUnmodifiedSince = !cp.IfUnmodifiedSinceTime.IsZero()
	cp.IsClientConditional = cp.HasIfNoneMatch || cp.HasIfModifiedSince || cp.HasIfUnmodifiedSince
}

// normalizeETag reduces an entity-tag to the opaque value weak comparison
// operates on, reporting whether the tag was marked weak. Both sides of a
// comparison must pass through it: a stored tag arrives quoted, the same way
// the origin sent it.
func normalizeETag(input string) (string, bool) {
	v := strings.TrimSpace(input)
	weak := strings.HasPrefix(v, "W/") || strings.HasPrefix(v, "w/")
	if weak {
		v = v[2:]
	}
	if len(v) > 1 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		v = v[1 : len(v)-1]
	}
	return v, weak
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

	want, _ := normalizeETag(etag)
	for p := range strings.SplitSeq(headerValue, ",") {
		// weak comparison ignores the W/ marker on either side
		if got, _ := normalizeETag(p); got == want {
			return false
		}
	}

	return true
}

// IfRangeMatches reports whether the client's If-Range validator still
// identifies the representation being served. RFC 9110 13.1.5 requires strong
// comparison, so a weak entity-tag never matches and a date must be exact.
func (cp *CachingPolicy) IfRangeMatches() bool {
	if cp.IfRangeValue == "" {
		return false
	}
	v := strings.TrimSpace(cp.IfRangeValue)
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "W/") || strings.HasPrefix(v, "w/") {
		want, weak := normalizeETag(v)
		if weak || cp.ETag == "" {
			return false
		}
		got, gotWeak := normalizeETag(cp.ETag)
		return !gotWeak && got == want
	}
	t, err := time.Parse(time.RFC1123, v)
	if err != nil {
		return false
	}
	return !cp.LastModified.IsZero() && cp.LastModified.Equal(t)
}
