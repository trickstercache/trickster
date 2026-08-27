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

package graphite

import (
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestOriginAuthHeader(t *testing.T) {
	tests := []struct {
		name     string
		g        *gro.Options
		expected string
		err      error
	}{
		{"nil options", nil, "", nil},
		{"unset", &gro.Options{}, "", nil},
		{"basic", &gro.Options{OriginUsername: "u", OriginPassword: "p"},
			"Basic dTpw", nil},
		{"username only", &gro.Options{OriginUsername: "u"}, "Basic dTo=", nil},
		{"raw", &gro.Options{OriginAuthorization: "Bearer tok"}, "Bearer tok", nil},
		{"conflict", &gro.Options{OriginAuthorization: "Bearer tok",
			OriginUsername: "u"}, "", gro.ErrOriginAuthConflict},
		{"password without username", &gro.Options{OriginPassword: "p"},
			"", gro.ErrOriginAuthNoUser},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := originAuthHeader(tc.g)
			if !errors.Is(err, tc.err) || out != tc.expected {
				t.Fatalf("got (%q, %v), want (%q, %v)", out, err, tc.expected, tc.err)
			}
		})
	}
}

func TestOriginAuthInjection(t *testing.T) {
	newAuthedOptions := func(user, pass string) *bo.Options {
		o := bo.New()
		o.Graphite = gro.New()
		o.Graphite.OriginUsername = user
		o.Graphite.OriginPassword = pass
		return o
	}

	o := newAuthedOptions("metrics", "s3cret")
	// a user-defined path with no pinned credential is injected; one with an
	// explicit pin is left alone
	plain := &po.Options{Path: "/render", Methods: methods.GetAndPost()}
	pinned := &po.Options{Path: "/tags", Methods: methods.GetAndPost(),
		RequestHeaders: map[string]string{"Authorization": "Basic other"}}
	o.Paths = po.List{plain, pinned}
	c := newTestClient(t, o)
	o.Paths = c.DefaultPathConfigs(o).Overlay(o.Paths)

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("metrics:s3cret"))
	for _, pc := range o.Paths {
		expected := want
		if pc.Path == "/tags" {
			expected = "Basic other"
		}
		if got := pc.RequestHeaders["Authorization"]; got != expected {
			t.Fatalf("%s: Authorization = %q, want %q", pc.Path, got, expected)
		}
		if pc.IdentityKeyPart() == "" {
			t.Fatalf("%s: identity key part must reflect the injected credential", pc.Path)
		}
	}

	// the health check default carries the credential
	if h := c.DefaultHealthCheckConfig().Headers["Authorization"]; h != want {
		t.Fatalf("health check Authorization = %q, want %q", h, want)
	}

	// synthetic identities are uniform, so a render carrying a client
	// Authorization header still accelerates: the credential replaces it
	r := getReq("target=a.b&from=-1h&format=json")
	r.Header.Set("Authorization", "Bearer client-token")
	r = request.SetResources(r, request.NewResources(nil, o.Paths.Match(http.MethodGet, "/render"),
		nil, nil, nil, nil))
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	rq, _ := trq.ParsedQuery.(*RenderQuery)
	if err != nil || rq.Fallback != "" {
		t.Fatalf("origin-authed backend must accelerate client-authed renders: %v %q", err, rq.Fallback)
	}

	// rotating the credential must rotate the effective identity digest, which
	// the daemon records at SetCache before default paths are overlaid
	ra := newTestClient(t, newAuthedOptions("metrics", "one"))
	rb := newTestClient(t, newAuthedOptions("metrics", "two"))
	if ra.effectiveIdentityDigest() == rb.effectiveIdentityDigest() {
		t.Fatal("rotating the origin credential must change the identity digest")
	}

	// an invalid combination fails construction
	bad := bo.New()
	bad.Graphite = gro.New()
	bad.Graphite.OriginPassword = "p"
	if _, err := NewClient("bad", bad, nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for origin_password without origin_username")
	}
}

func TestOriginAuthAppendRejected(t *testing.T) {
	// a set and an append on Authorization would share one unordered map, so
	// a path appending it alongside an origin credential fails construction
	for _, key := range []string{"+Authorization", "+authorization"} {
		o := bo.New()
		o.Graphite = gro.New()
		o.Graphite.OriginAuthorization = "Bearer backend-token"
		o.Paths = po.List{{Path: "/render", Methods: methods.GetAndPost(),
			RequestHeaders: map[string]string{key: "secondary-value"}}}
		if _, err := NewClient("test", o, nil, nil, nil, nil); !errors.Is(err,
			gro.ErrOriginAuthAppend) {
			t.Fatalf("%s: expected ErrOriginAuthAppend, got %v", key, err)
		}
	}

	// without a credential the append is permitted and untouched
	o := bo.New()
	o.Graphite = gro.New()
	pc := &po.Options{Path: "/render", Methods: methods.GetAndPost(),
		RequestHeaders: map[string]string{"+Authorization": "secondary-value"}}
	o.Paths = po.List{pc}
	if _, err := NewClient("test", o, nil, nil, nil, nil); err != nil {
		t.Fatalf("append without origin credential must construct: %v", err)
	}
	if len(pc.RequestHeaders) != 1 {
		t.Fatalf("path headers must be untouched, got %v", pc.RequestHeaders)
	}
}

func TestOriginAuthDeterministicHeader(t *testing.T) {
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.OriginAuthorization = "Bearer backend-token"
	c := newTestClient(t, o)
	o.Paths = c.DefaultPathConfigs(o).Overlay(o.Paths)
	pc := o.Paths.Match(http.MethodGet, "/render")

	// proxied and synthetic requests share the path's header map; repeated
	// applications must always produce exactly one Authorization value
	wantID := pc.IdentityKeyPart()
	for range 100 {
		r := getReq("target=a.b&from=-1h&format=json")
		r.Header.Set("Authorization", "Bearer client-token")
		headers.UpdateRequestHeaders(r, pc.RequestHeaders)
		if v := r.Header.Values("Authorization"); len(v) != 1 || v[0] != "Bearer backend-token" {
			t.Fatalf("Authorization values = %v, want exactly [Bearer backend-token]", v)
		}
		if pc.IdentityKeyPart() != wantID {
			t.Fatal("cache identity must be stable across requests")
		}
	}
	if render, expand := c.synthIdentities(); render != wantID || expand != wantID {
		t.Fatal("synthetic requests must share the proxied request identity")
	}
}
