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
		name        string
		a           http.Header
		expectedTTL time.Duration
	}{
		{
			name: "no-store",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoStore},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "no-cache",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoCache},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "max-age 300",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge + "=300"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{
			name: "max-age empty value",
			// Cache-Control: max-age=   should come back as -1 ttl
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge + "="},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "max-age no value",
			// Cache-Control: max-age (no =anything)  should come back as -1 ttl
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMaxAge},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "private with max-age",
			// private,max-age=300 should be treated as non-cacheable by proxy
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePrivate + "," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "public with max-age",
			// public,max-age=300 should be treated as cacheable by proxy
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{
			name: "cache-control wins over expires",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
				headers.NameExpires:      []string{"-1"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{
			name: "cache-control wins over last-modified",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValuePublic + "," + headers.ValueMaxAge + "=300"},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: time.Minute * time.Duration(5),
		},
		{
			name: "expires unparsable past",
			a: http.Header{
				headers.NameExpires: []string{"-1"},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "expires parseable past",
			a: http.Header{
				headers.NameExpires: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "expires in one hour",
			a: http.Header{
				headers.NameDate:    []string{now.UTC().Format(time.RFC1123)},
				headers.NameExpires: []string{now.Add(time.Hour * time.Duration(1)).UTC().Format(time.RFC1123)},
			},
			expectedTTL: 1 * time.Hour,
		},
		{
			name: "synthesized from last-modified",
			a: http.Header{
				headers.NameDate:         []string{now.UTC().Format(time.RFC1123)},
				headers.NameLastModified: []string{now.Add(-time.Hour * time.Duration(5)).UTC().Format(time.RFC1123)},
			},
			expectedTTL: 1 * time.Hour,
		},
		{
			name: "no cache headers",
			a: http.Header{
				headers.NameDate: []string{now.UTC().Format(time.RFC1123)},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "invalid date format",
			a: http.Header{
				headers.NameDate:    []string{"1571338193"},
				headers.NameExpires: []string{"-1"},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "etag only",
			a: http.Header{
				headers.NameETag: []string{"etag-test"},
			},
			expectedTTL: 0,
		},
		{
			name: "invalid last-modified format",
			a: http.Header{
				headers.NameLastModified: []string{"1571338193"},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "must-revalidate",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueMustRevalidate},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: 0,
		},
		{
			name: "no-transform",
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueNoTransform},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "set-cookie",
			a: http.Header{
				headers.NameSetCookie: []string{"some-fake-value-for-testing"},
			},
			expectedTTL: -1 * time.Second,
		},
		{
			name: "s-maxage only",
			// s-maxage=600 should be used for shared cache TTL
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueSharedMaxAge + "=600"},
			},
			expectedTTL: 10 * time.Minute,
		},
		{
			name: "s-maxage overrides max-age",
			// s-maxage takes precedence over max-age for shared caches
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueSharedMaxAge + "=600," + headers.ValueMaxAge + "=300"},
			},
			expectedTTL: 10 * time.Minute,
		},
		{
			name: "proxy-revalidate",
			// proxy-revalidate should set MustRevalidate, TTL=0
			a: http.Header{
				headers.NameCacheControl: []string{headers.ValueProxyRevalidate},
				headers.NameLastModified: []string{"Sun, 16 Jun 2019 14:19:04 GMT"},
			},
			expectedTTL: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
	p := GetResponseCachingPolicy(400, map[int]time.Duration{400: 5 * time.Minute}, nil)
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
	tests := []struct {
		name string
		etag string
		inm  string // If-None-Match header value
		ls   status.LookupStatus
		want bool
	}{
		{"both empty", "", "", status.LookupStatusHit, true},
		{"etag empty inm not", "", "test", status.LookupStatusHit, false},
		{"inm empty etag not", "test", "", status.LookupStatusHit, false},
		{"wildcard match on hit", "test", "*", status.LookupStatusHit, false},
		{"wildcard on revalidated", "test", "*", status.LookupStatusRevalidated, false},
		{"wildcard on miss", "test", "*", status.LookupStatusKeyMiss, true},
		{"exact match", "test", "test", status.LookupStatusHit, false},
		{"weak match", "test", "w/test", status.LookupStatusHit, false},
		{"quoted match", "test", `"test"`, status.LookupStatusHit, false},
		{"multi-value one matches", "test", `"foo", "test"`, status.LookupStatusHit, false},
		{"multi-value none match", "test", `"foo", "bar"`, status.LookupStatusHit, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckIfNoneMatch(tt.etag, tt.inm, tt.ls); got != tt.want {
				t.Errorf("expected %t got %t", tt.want, got)
			}
		})
	}
}

// TestParseCacheControlNoTransform verifies that the no-transform directive
// is correctly parsed and sets the NoTransform flag on the caching policy.
func TestParseCacheControlNoTransform(t *testing.T) {
	h := http.Header{
		headers.NameCacheControl: []string{headers.ValueNoTransform + ",max-age=300"},
	}
	cp := GetResponseCachingPolicy(200, nil, h)
	if !cp.NoTransform {
		t.Error("expected NoTransform=true")
	}
	// verify max-age is still parsed alongside no-transform
	if cp.FreshnessLifetime != 300 {
		t.Errorf("expected FreshnessLifetime=300, got %d", cp.FreshnessLifetime)
	}
}
