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
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/md5"
)

// Cache Lookup Results
const (
	CrKeyMiss    = "kmiss"
	CrRangeMiss  = "rmiss"
	CrHit        = "hit"
	CrPartialHit = "phit"
	CrPurge      = "purge"
)

// HTTP Cache-Control Directives
const (
	// For both HTTP Requests and Responses
	ccdNoCache     = "no-cache"
	ccdNoStore     = "no-store"
	ccdMaxAge      = "max-age" // implemented in response only
	ccdNoTransform = "no-transform"

	// For HTTP Responses Only
	ccdPrivate         = "private"
	ccdPublic          = "public"
	ccdMustRevalidate  = "must-revalidate"
	ccdProxyRevalidate = "proxy-revalidate"
	ccdSharedMaxAge    = "s-maxage"

	// For HTTP Requests Only - not currently implemented
	ccdMinFresh     = "min-fresh"
	ccdMaxStale     = "max-stale"
	ccdOnlyIfCached = "only-if-cached"
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
	_, err = d.UnmarshalMsg(bytes)
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
func DeriveCacheKey(c model.Client, cfg *config.OriginConfig, r *model.Request, extra string) string {
	var hashParams []string
	var hashHeaders []string

	matchLen := -1
	for k, p := range cfg.Paths {
		if strings.Index(r.URL.Path, k) > -1 && len(k) > matchLen {
			matchLen = len(k)
			hashParams = p.CacheKeyParams
			hashHeaders = p.CacheKeyHeaders
		}
	}

	params := r.URL.Query()
	vals := make([]string, 0, len(hashParams)+len(hashHeaders))

	for _, p := range hashParams {
		if v := params.Get(p); v != "" {
			vals = append(vals, v)
		}
	}

	for _, p := range hashHeaders {
		if v := r.Headers.Get(p); v != "" {
			vals = append(vals, v)
		}
	}

	return md5.Checksum(r.URL.Path + strings.Join(vals, "") + extra)
}

// GetResponseCachingPolicy examines HTTP response headers for caching headers
// a returns a CachingPolicy reference
func GetResponseCachingPolicy(code int, negativeCache map[int]time.Duration, h http.Header) *model.CachingPolicy {

	cp := &model.CachingPolicy{LocalDate: time.Now()}

	if d, ok := negativeCache[code]; ok {
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
	if v, ok := lch[headers.NameCacheControl]; ok {
		parseCacheControlDirectives(strings.Join(v, ","), cp)
	}

	if cp.NoCache {
		return cp
	}

	_, hasLastModified := lch[headers.NameExpires]
	_, hasExpires := lch[headers.NameLastModified]
	_, hasETag := lch[headers.NameETag]

	// Get the date header or, if it is not found or parsed, set it
	if v, ok := lch[headers.NameDate]; ok {
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

	// otherwise look for expiration and/or validators
	if cp.FreshnessLifetime > 0 || hasExpires {

		// no Max-Age provided yet, look for expires
		if cp.FreshnessLifetime == 0 {
			// if there is an Expires header, respect it
			if v, ok := lch[headers.NameExpires]; ok {
				expiresHeader := strings.Join(v, "")
				expires, err := time.Parse(time.RFC1123, expiresHeader)
				if err == nil {
					cp.Expires = expires
					cp.FreshnessLifetime = int(cp.Expires.Sub(cp.Date).Seconds())
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
			cp.ETag = strings.Join(lch[headers.NameETag], "")
		}

		if v, ok := lch[headers.NameLastModified]; ok {
			mnHeader := strings.Join(v, "")
			lm, err := time.Parse(time.RFC1123, mnHeader)
			if err != nil {
				cp.CanRevalidate = false
			} else {
				cp.LastModified = lm
			}
		}

		// else, if there is a Last-Modified header, set FreshnessLifetime to 20% of age
		if cp.CanRevalidate && cp.FreshnessLifetime == 0 && !cp.LastModified.IsZero() && cp.LastModified.Before(cp.Date) {
			objectAge := int(cp.Date.Sub(cp.LastModified).Seconds())
			if objectAge > 0 {
				cp.FreshnessLifetime = objectAge / 5
			}
		}
	}
	return cp
}

var supportedCCD = map[string]bool{
	ccdPrivate:         true,
	ccdNoCache:         true,
	ccdNoStore:         true,
	ccdMaxAge:          false,
	ccdSharedMaxAge:    false,
	ccdMustRevalidate:  false,
	ccdProxyRevalidate: false,
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
			return
		}
		if d == ccdSharedMaxAge && dsub != "" {
			foundFreshnessDirective = true
			secs, err := strconv.Atoi(dsub)
			if err == nil {
				hasSharedMaxAge = true
				cp.FreshnessLifetime = secs
			}
		}
		if (!hasSharedMaxAge) && d == ccdMaxAge && dsub != "" {
			foundFreshnessDirective = true
			secs, err := strconv.Atoi(dsub)
			if err == nil {
				cp.FreshnessLifetime = secs
			}
		}
		if (d == ccdMustRevalidate || d == ccdProxyRevalidate) || (cp.FreshnessLifetime == 0 && foundFreshnessDirective) {
			cp.MustRevalidate = true
		}
		if d == ccdNoTransform {
			cp.NoTransform = true
		}
	}
}

func hasPragmaNoCache(h http.Header) bool {
	if v, ok := h[headers.NamePragma]; ok {
		return strings.ToLower(strings.Join(v, ",")) == ccdNoCache
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
	if v, ok := lch[headers.NameCacheControl]; ok {
		parseCacheControlDirectives(strings.Join(v, ","), cp)
		if cp.NoCache {
			return cp
		}
	}

	if v, ok := lch[headers.NameIfModifiedSince]; ok {
		if date, err := time.Parse(time.RFC1123, strings.Join(v, "")); err == nil {
			cp.IfModifiedSinceTime = date
		}
	}

	if v, ok := lch[headers.NameIfUnmodifiedSince]; ok {
		if date, err := time.Parse(time.RFC1123, strings.Join(v, "")); err == nil {
			cp.IfUnmodifiedSinceTime = date
		}
	}

	if v, ok := lch[headers.NameIfNoneMatch]; ok {
		cp.IfNoneMatchValue = strings.Join(v, "")
	}

	if v, ok := lch[headers.NameIfMatch]; ok {
		cp.IfMatchValue = strings.Join(v, "")
	}

	return cp
}
