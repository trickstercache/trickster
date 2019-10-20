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

	"github.com/golang/snappy"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/md5"
)

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(c cache.Cache, key string) (*model.HTTPDocument, error) {

	inflate := c.Configuration().Compression
	if inflate {
		key += ".sz"
	}

	d := &model.HTTPDocument{}
	bytes, err := c.Retrieve(key, true)
	if err != nil {
		return d, err
	}

	if inflate {
		log.Debug("decompressing cached data", log.Pairs{"cacheKey": key})
		b, err := snappy.Decode(nil, bytes)
		if err == nil {
			bytes = b
		}
	}
	d.UnmarshalMsg(bytes)
	return d, nil
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(c cache.Cache, key string, d *model.HTTPDocument, ttl time.Duration) error {
	// Delete Date Header, http.ReponseWriter will insert as Now() on cache retrieval
	delete(d.Headers, "Date")
	bytes, err := d.MarshalMsg(nil)
	if err != nil {
		return err
	}

	if c.Configuration().Compression {
		key += ".sz"
		log.Debug("compressing cached data", log.Pairs{"cacheKey": key})
		bytes = snappy.Encode(nil, bytes)
	}

	return c.Store(key, bytes, ttl)
}

// DeriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func DeriveCacheKey(c model.Client, r *model.Request, apc *config.PathConfig, extra string) string {

	pc := context.PathConfig(r.ClientRequest.Context())
	if apc != nil {
		pc = apc
	}
	if pc == nil {
		return md5.Checksum(r.URL.Path + extra)
	}

	params := r.URL.Query()
	vals := make([]string, 0, len(pc.CacheKeyParams)+len(pc.CacheKeyHeaders))

	if len(pc.CacheKeyParams) == 1 && pc.CacheKeyParams[0] == "*" {
		for p := range params {
			vals = append(vals, []string{p, params.Get(p)}...)
		}
	} else {
		for _, p := range pc.CacheKeyParams {
			if v := params.Get(p); v != "" {
				vals = append(vals, []string{p, v}...)
			}
		}
	}

	for _, p := range pc.CacheKeyHeaders {
		if v := r.Headers.Get(p); v != "" {
			vals = append(vals, []string{p, v}...)
		}
	}

	return md5.Checksum(r.URL.Path + strings.Join(vals, "") + extra)
}

// GetResponseCachingPolicy examines HTTP response headers for caching headers
// a returns a CachingPolicy reference
func GetResponseCachingPolicy(code int, negativeCache map[int]time.Duration, h http.Header) *model.CachingPolicy {

	cp := &model.CachingPolicy{LocalDate: time.Now()}

	if d, ok := negativeCache[code]; ok {
		cp.FreshnessLifetime = int(d.Seconds())
		cp.Expires = cp.LocalDate.Add(d)
		return cp
	}

	// make a lowercase copy of the headers
	// to allow for quick map lookups on both http/1.x and http/2
	lch := make(http.Header)
	for k, v := range h {
		lch[strings.ToLower(k)] = v
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

	// TODO: Flush out real etag support (e.g., lists for if-non-match, *, W/, etc.)
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
