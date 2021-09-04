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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestCachingPolicyClone(t *testing.T) {
	cp := &CachingPolicy{
		IsClientFresh: true,
	}
	v := cp.Clone().IsClientFresh
	if !v {
		t.Errorf("expected %t got %t", true, v)
	}
}

func TestMerge(t *testing.T) {

	cp := &CachingPolicy{
		IsClientFresh: true,
	}

	cp.Merge(nil)
	if !cp.IsClientFresh {
		t.Errorf("expected %t got %t", true, cp.IsClientFresh)
	}

}

func TestGetResponseCachingPolicy(t *testing.T) {

	now := time.Now().Truncate(time.Second)

	tests := []struct {
		a           http.Header
		expectedTTL time.Duration
	}{
		{ // 0 - Cache-Control: no-store
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoStore},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 1 -  Cache-Control: no-cache
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoCache},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 2 - Cache-Control: max-age=300
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge + "=300"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 3 - Cache-Control: max-age=   should come back as -1 ttl
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge + "="},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 4 - Cache-Control: max-age (no =anything)  should come back as 0 ttl
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 5 - Cache-Control: private,max-age=300  should be treated as non-cacheable by proxy
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePrivate + "," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 6 - Cache-Control: public,max-age=300  should be treated as cacheable by proxy
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 7 - Cache-Control and Expires, Cache-Control should win
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
				headers.NameExpires:      []string{"-1"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 8 - Cache-Control and LastModified, Cache-Control should win
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{ // 9 - Already Expired (could not parse)
			a: http.Header{
				headers.NameExpires: []string{"-1"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 10 - Already Expired (parseable in the past)
			a: http.Header{
				headers.NameExpires: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 11 - Expires in an hour
			a: http.Header{
				headers.NameDate:    []string{now.UTC().Format(time.RFC1123)},
				headers.NameExpires: []string{now.Add(time.Hour * time.Duration(1)).UTC().Format(time.RFC1123)},
			},
			expectedTTL: 1 * time.Hour,
		},
		{ // 12 - Synthesized TTL from Last Modified
			a: http.Header{
				headers.NameDate:         []string{now.UTC().Format(time.RFC1123)},
				headers.NameLastModified: []string{now.Add(-time.Hour * time.Duration(5)).UTC().Format(time.RFC1123)},
			},
			expectedTTL: 1 * time.Hour,
		},
		{ // 13 - No Cache Control Response Headers
			a: http.Header{
				headers.NameDate: []string{now.UTC().Format(time.RFC1123)},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 14 - Invalid Date Header Format
			a: http.Header{
				headers.NameDate:    []string{"1571338193"},
				headers.NameExpires: []string{"-1"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 15 - Invalid Date Header Format
			a: http.Header{
				headers.NameETag: []string{"etag-test"},
			},
			expectedTTL: 0,
		},
		{ // 16 - Invalid Last Modified Date Header Format
			a: http.Header{
				headers.NameLastModified: []string{"1571338193"},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 17 - Must Revalidate
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMustRevalidate},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: 0,
		},
		{ // 18 - NoTransform
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoTransform},
			},
			expectedTTL: -1 * time.Second,
		},
		{ // 19 - Set-Cookie
			a: http.Header{
				headers.NameSetCookie: []string{"some-fake-value-for-testing"},
			},
			expectedTTL: -1 * time.Second,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			p := GetResponseCachingPolicy(200, nil, test.a)
			d := time.Duration(p.FreshnessLifetime) * time.Second
			if test.expectedTTL != d {
				t.Errorf("expected ttl of %d got %d", test.expectedTTL, d)
			}
		})
	}
}

func TestResolveClientConditionalsIUS(t *testing.T) {

	cp := &CachingPolicy{
		IsClientConditional:   true,
		HasIfUnmodifiedSince:  true,
		LastModified:          time.Unix(5, 0),
		IfUnmodifiedSinceTime: time.Unix(4, 0),
	}
	cp.ResolveClientConditionals(status.LookupStatusHit)

	if !cp.IsClientFresh {
		t.Errorf("expected %t got %t", true, cp.IsClientFresh)
	}

}

func TestGetResponseCachingPolicyNegativeCache(t *testing.T) {
	p := GetResponseCachingPolicy(400, map[int]time.Duration{400: 300 * time.Second}, nil)
	if p.FreshnessLifetime != 300 {
		t.Errorf("expected ttl of %d got %d", 300, p.FreshnessLifetime)
	}
}

func TestGetRequestCacheability(t *testing.T) {

	tests := []struct {
		a           http.Header
		isCacheable bool
	}{
		{ // 0 - Cache-Control: no-store
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoStore},
			},
			isCacheable: false,
		},
		{ // 1 -  Cache-Control: no-cache
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoCache},
			},
			isCacheable: false,
		},
		{ // 2 - No Cache Control Request Headers
			a:           http.Header{},
			isCacheable: true,
		},
		{ // 3 - Pragma: NoCache
			a: http.Header{
				headers.NamePragma: []string{headers.ValueNoCache},
			},
			isCacheable: false,
		},
		{ // 4 - IMS
			a: http.Header{
				headers.NameIfModifiedSince: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			isCacheable: true,
		},
		{ // 5 - IUS
			a: http.Header{
				headers.NameIfUnmodifiedSince: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			isCacheable: true,
		},
		{ // 6 - INM
			a: http.Header{
				headers.NameIfNoneMatch: []string{"test-string"},
			},
			isCacheable: true,
		},
		{ // 7 - IM
			a: http.Header{
				headers.NameIfMatch: []string{"test-string"},
			},
			isCacheable: true,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			p := GetRequestCachingPolicy(test.a)
			ic := !p.NoCache
			if ic != test.isCacheable {
				t.Errorf("mismatch isCacheable expected %v got %v", test.isCacheable, ic)
			}
		})
	}

}

func TestCheckIfNoneMatch(t *testing.T) {

	res := CheckIfNoneMatch("", "", status.LookupStatusHit)
	if !res {
		t.Errorf("expected %t got %t", true, res)
	}

	res = CheckIfNoneMatch("test", "*", status.LookupStatusHit)
	if res {
		t.Errorf("expected %t got %t", false, res)
	}

	res = CheckIfNoneMatch("test", "*", status.LookupStatusKeyMiss)
	if !res {
		t.Errorf("expected %t got %t", true, res)
	}

	res = CheckIfNoneMatch("test", "test", status.LookupStatusHit)
	if res {
		t.Errorf("expected %t got %t", false, res)
	}

	res = CheckIfNoneMatch("test", "w/test", status.LookupStatusHit)
	if res {
		t.Errorf("expected %t got %t", false, res)
	}

}
