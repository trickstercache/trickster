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

package urls

import (
	"net/http"
	"net/url"
	"testing"
)

func TestBuildUpstreamURLAppliesExplicitRewrites(t *testing.T) {
	base, err := url.Parse("http://origin.example.com:9090/base")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		rewrite func(*http.Request)
		wantURL string
	}{
		{
			name:    "inbound host is ignored without a rewrite",
			wantURL: "http://origin.example.com:9090/base/path?query=value",
		},
		{
			name: "scheme",
			rewrite: func(r *http.Request) {
				SetUpstreamScheme(r, "https")
			},
			wantURL: "https://origin.example.com:9090/base/path?query=value",
		},
		{
			name: "host",
			rewrite: func(r *http.Request) {
				SetUpstreamHost(r, "other.example.com:7070")
			},
			wantURL: "http://other.example.com:7070/base/path?query=value",
		},
		{
			name: "hostname preserves origin port",
			rewrite: func(r *http.Request) {
				SetUpstreamHostname(r, "tenant.origin.example.com")
			},
			wantURL: "http://tenant.origin.example.com:9090/base/path?query=value",
		},
		{
			name: "port preserves origin hostname",
			rewrite: func(r *http.Request) {
				SetUpstreamPort(r, "8443")
			},
			wantURL: "http://origin.example.com:8443/base/path?query=value",
		},
		{
			name: "later host supersedes component rewrites",
			rewrite: func(r *http.Request) {
				SetUpstreamHostname(r, "tenant.origin.example.com")
				SetUpstreamPort(r, "8443")
				SetUpstreamHost(r, "final.example.com:6060")
			},
			wantURL: "http://final.example.com:6060/base/path?query=value",
		},
		{
			name: "components update a prior host rewrite",
			rewrite: func(r *http.Request) {
				SetUpstreamHost(r, "first.example.com:7070")
				SetUpstreamHostname(r, "final.example.com")
				SetUpstreamPort(r, "6060")
			},
			wantURL: "http://final.example.com:6060/base/path?query=value",
		},
		{
			name: "empty port removes origin port",
			rewrite: func(r *http.Request) {
				SetUpstreamPort(r, "")
			},
			wantURL: "http://origin.example.com/base/path?query=value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet,
				"http://trickster.example.com:8480/path?query=value", nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.rewrite != nil {
				test.rewrite(r)
			}

			got := BuildUpstreamURL(r, base)
			if got.String() != test.wantURL {
				t.Errorf("URL = %q, want %q", got.String(), test.wantURL)
			}
			if got := base.String(); got != "http://origin.example.com:9090/base" {
				t.Errorf("base URL was modified: %q", got)
			}
		})
	}
}

func TestUpstreamURLRewritesAreIsolatedAfterClone(t *testing.T) {
	base, err := url.Parse("http://origin.example.com:9090")
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodGet, "http://trickster.example.com/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	SetUpstreamHostname(r, "parent.example.com")

	clone := r.Clone(r.Context())
	SetUpstreamHostname(clone, "clone.example.com")

	if got, want := BuildUpstreamURL(r, base).Host, "parent.example.com:9090"; got != want {
		t.Errorf("parent host = %q, want %q", got, want)
	}
	if got, want := BuildUpstreamURL(clone, base).Host, "clone.example.com:9090"; got != want {
		t.Errorf("clone host = %q, want %q", got, want)
	}
}

func TestUpstreamURLRewriteSupportsIPv6(t *testing.T) {
	base, err := url.Parse("http://[2001:db8::1]:9090")
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodGet, "http://trickster.example.com/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	SetUpstreamHostname(r, "2001:db8::2")

	if got, want := BuildUpstreamURL(r, base).Host, "[2001:db8::2]:9090"; got != want {
		t.Errorf("host = %q, want %q", got, want)
	}
}

func TestSetUpstreamURLRewriteWithNilRequest(t *testing.T) {
	SetUpstreamScheme(nil, "https")
	SetUpstreamHost(nil, "example.com")
	SetUpstreamHostname(nil, "example.com")
	SetUpstreamPort(nil, "443")
	if got := UpstreamURLRewriteCacheKey(nil, nil); got != "" {
		t.Errorf("cache key = %q, want empty", got)
	}
}
