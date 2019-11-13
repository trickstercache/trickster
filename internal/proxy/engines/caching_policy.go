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
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// GetResponseCachingPolicy examines HTTP response headers for caching headers
// a returns a CachingPolicy reference
func GetResponseCachingPolicy(code int, negativeCache map[int]time.Duration, h http.Header) *model.CachingPolicy {

	cp := &model.CachingPolicy{LocalDate: time.Now()}

	if d, ok := negativeCache[code]; ok {
		cp.FreshnessLifetime = int(d.Seconds())
		cp.Expires = cp.LocalDate.Add(d)
		cp.IsNegativeCache = true
		return cp
	}

	// make a lowercase copy of the headers
	// to allow for quick map lookups on both http/1.x and http/2
	lch := make(http.Header)
	for k, v := range h {
		lch[strings.ToLower(k)] = v
	}

	// Do not cache content that includes set-cookie header
	// Trickster can use PathConfig rules to strip set-cookie if cachablility is needed
	if _, ok := lch["set-cookie"]; ok {
		cp.NoCache = true
		cp.FreshnessLifetime = -1
		return cp
	}

	// Cache-Control has first precedence
	if v, ok := lch["cache-control"]; ok {
		parseCacheControlDirectives(strings.Join(v, ","), cp)
	}

	if cp.NoCache {
		cp.FreshnessLifetime = -1
		return cp
	}

	_, hasLastModified := lch["last-modified"]
	exp, hasExpires := lch["expires"]
	_, hasETag := lch["etag"]

	if !hasLastModified && !hasExpires && !hasETag && cp.FreshnessLifetime == 0 {
		cp.NoCache = true
		cp.FreshnessLifetime = -1
		return cp
	}

	// Get the date header or, if it is not found or parsed, set it
	if v, ok := lch["date"]; ok {
		if date, err := time.Parse(time.RFC1123, strings.Join(v, "")); err != nil {
			cp.Date = cp.LocalDate
			h.Set(headers.NameDate, cp.Date.Format(time.RFC1123))
		} else {
			cp.Date = date
		}
	} else {
		cp.Date = cp.LocalDate
		h.Set(headers.NameDate, cp.Date.Format(time.RFC1123))
	}

	// no Max-Age provided yet, look for expires
	if cp.FreshnessLifetime == 0 && !cp.MustRevalidate {
		// if there is an Expires header, respect it
		if hasExpires {
			expiresHeader := strings.Join(exp, "")
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
		cp.ETag = strings.Join(lch["etag"], "")
	}

	if v, ok := lch["last-modified"]; ok {
		mnHeader := strings.Join(v, "")
		lm, err := time.Parse(time.RFC1123, mnHeader)
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

func parseCacheControlDirectives(directives string, cp *model.CachingPolicy) {
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
		if (d == headers.ValueMustRevalidate || d == headers.ValueProxyRevalidate) || (cp.FreshnessLifetime == 0 && foundFreshnessDirective) {
			cp.MustRevalidate = true
			cp.FreshnessLifetime = 0
		}
		if d == headers.ValueNoTransform {
			cp.NoTransform = true
		}
	}

}

func hasPragmaNoCache(h http.Header) bool {
	if v, ok := h[headers.NamePragma]; ok {
		return strings.ToLower(strings.Join(v, ",")) == headers.ValueNoCache
	}
	return false
}

// GetRequestCachingPolicy examines HTTP request headers for caching headers
// and true if the corresponding response is OK to cache
func GetRequestCachingPolicy(h http.Header) *model.CachingPolicy {
	cp := &model.CachingPolicy{LocalDate: time.Now()}
	// make a lowercase copy of the headers
	// to allow for quick map lookups on both http/1.x and http/2
	lch := make(http.Header)
	for k, v := range h {
		lch[strings.ToLower(k)] = v
	}
	if hasPragmaNoCache(lch) {
		cp.NoCache = true
		return cp
	}
	if v, ok := lch["cache-control"]; ok {
		parseCacheControlDirectives(strings.Join(v, ","), cp)
		if cp.NoCache {
			return cp
		}
	}

	if v, ok := lch["if-modified-since"]; ok {
		if date, err := time.Parse(time.RFC1123, strings.Join(v, "")); err == nil {
			cp.IfModifiedSinceTime = date
		}
	}

	if v, ok := lch["if-unmodified-since"]; ok {
		if date, err := time.Parse(time.RFC1123, strings.Join(v, "")); err == nil {
			cp.IfUnmodifiedSinceTime = date
		}
	}

	if v, ok := lch["if-none-match"]; ok {
		cp.IfNoneMatchValue = strings.Join(v, "")
	}

	if v, ok := lch["if-match"]; ok {
		cp.IfMatchValue = strings.Join(v, "")
	}

	return cp
}
