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
	"net/http"
	"sort"
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
func QueryCache(c cache.Cache, key string, byteRange model.Ranges) (*model.HTTPDocument, error) {

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
	if byteRange != nil {
		d.UpdatedQueryRange = d.Ranges.CalculateDelta(d, byteRange)
		// Cache hit
		if d.UpdatedQueryRange == nil {
			body := d.Body
			d.Body = make([]byte, len(d.Body))
			for _, v := range byteRange {
				copy(d.Body[v.Start:v.End], body[v.Start:v.End])
			}
		}
	}
	return d, nil
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(c cache.Cache, key string, d *model.HTTPDocument, ttl time.Duration, byteRange model.Ranges) error {
	// Delete Date Header, http.ReponseWriter will insert as Now() on cache retrieval
	delete(d.Headers, "Date")
	if byteRange == nil {
		ranges := make(model.Ranges, 1)
		if d.Headers["Content-Length"] != nil {
			end, err := strconv.Atoi(d.Headers["Content-Length"][0])
			if err != nil {
				log.Error("Couldn't convert the content length to a number", log.Pairs{"content length": end})
				return err
			}
			fullByteRange := model.Range{Start: 0, End: end}
			ranges[0] = fullByteRange
			d.Ranges = ranges
		}
	}
	bytes, err := d.MarshalMsg(nil)
	if err != nil {
		return err
	}

	if c.Configuration().Compression {
		key += ".sz"
		log.Debug("compressing cached data", log.Pairs{"cacheKey": key})
		bytes = snappy.Encode(nil, bytes)
	}

	if byteRange != nil {
		// Content-Range
		doc, err := QueryCache(c, key, byteRange)
		if err != nil {
			// First time, Doesn't exist in the cache
			// Example -> Content-Range: bytes 0-1023/146515
			// length = 0-1023/146515
			length := d.Headers["Content-Range"][0]
			index := 0
			for k, v := range length {
				if '/' == v {
					index = k
					break
				}
			}
			// length, after this, will have 146515
			length = length[index+1:]
			totalSize, err := strconv.Atoi(length)
			if err != nil {
				log.Error("Couldn't convert to a valid length", log.Pairs{"length": length})
			}
			fullSize := make([]byte, totalSize)

			// Multipart request
			if d.Headers["Content-Type"] != nil {
				if strings.Contains(d.Headers["Content-Type"][0], "multipart/byteranges; boundary=") {
					for _, v2 := range byteRange {
						start := v2.Start
						end := v2.End
						copy(fullSize[start:end], d.Body[start:end])
					}
				} else {
					copy(fullSize[byteRange[0].Start:byteRange[0].End], d.Body)
				}
			}

			d.Body = fullSize
			d.Ranges = byteRange
			bytes, err = d.MarshalMsg(nil)
			if err != nil {
				return err
			}
			return c.Store(key, bytes, ttl)
		}
		// Case when the key was found in the cache: store only the required part
		for _, v3 := range doc.UpdatedQueryRange {
			doc.Ranges[len(doc.Ranges)-1] = model.Range{Start: v3.Start, End: v3.End}
		}
		doc.UpdatedQueryRange = nil
		bytes, err = d.MarshalMsg(nil)
		if err != nil {
			return err
		}
		return c.Store(key, bytes, ttl)
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

	if pc.KeyHasher != nil && len(pc.KeyHasher) == 1 {
		return pc.KeyHasher[0](r.URL.Path, params, r.Headers, r.ClientRequest.Body, extra)
	}

	vals := make([]string, 0, (len(pc.CacheKeyParams) + len(pc.CacheKeyHeaders)*2))

	// Append the http method to the map for creating the derived cache key
	vals = append(vals, fmt.Sprintf("%s.%s.", "method", r.HTTPMethod))

	if len(pc.CacheKeyParams) == 1 && pc.CacheKeyParams[0] == "*" {
		for p := range params {
			vals = append(vals, fmt.Sprintf("%s.%s.", p, params.Get(p)))
		}
	} else {
		for _, p := range pc.CacheKeyParams {
			if v := params.Get(p); v != "" {
				vals = append(vals, fmt.Sprintf("%s.%s.", p, v))
			}
		}
	}

	for _, p := range pc.CacheKeyHeaders {
		if v := r.Headers.Get(p); v != "" {
			vals = append(vals, fmt.Sprintf("%s.%s.", p, v))
		}
	}

	sort.Strings(vals)
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
