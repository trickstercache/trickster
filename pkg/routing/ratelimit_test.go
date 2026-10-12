/*
 * Copyright 2026 The Trickster Authors
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

package routing

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	albopts "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	geoacl "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type markAuth struct{ saw bool }

func (m *markAuth) Authenticate(r *http.Request) (*types.AuthResult, error) {
	if r.Header.Get(headers.NameXTricksterRateLimited) != "" {
		m.saw = true
	}
	return &types.AuthResult{Status: types.AuthSuccess}, nil
}
func (markAuth) ExtractCredentials(*http.Request) (string, string, error)  { return "", "", nil }
func (markAuth) SetExtractCredentialsFunc(types.ExtractCredsFunc)          {}
func (markAuth) SetCredentials(*http.Request, string, string) error        { return nil }
func (markAuth) SetSetCredentialsFunc(types.SetCredentialsFunc)            {}
func (markAuth) SetObserveOnly(bool)                                       {}
func (markAuth) IsObserveOnly() bool                                       { return false }
func (markAuth) LoadUsers(string, types.CredentialsFileFormat, bool) error { return nil }
func (markAuth) AddUser(string, string) error                              { return nil }
func (markAuth) RemoveUser(string)                                         {}
func (m *markAuth) Clone() types.Authenticator                             { return m }
func (markAuth) ProxyPreserve() bool                                       { return true }
func (markAuth) Sanitize(*http.Request)                                    {}

func TestRouteRateLimitPlacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		saw  bool
	}{
		{name: "outside-auth", keys: []string{"method"}, saw: true},
		{name: "inside-auth", keys: []string{"user"}, saw: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &options.Options{
				Name: tc.name, Limit: 1, Window: timeconv.Duration(time.Minute),
				Action: options.ActionCount, Keys: tc.keys, MissingKey: options.MissingShared,
			}
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
			auth := &markAuth{}
			var atApp bool
			app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atApp = r.Header.Get(headers.NameXTricksterRateLimited) != ""
				w.WriteHeader(http.StatusOK)
			})
			path := &po.Options{
				RateLimiterName: tc.name, RateLimiter: o,
				AuthOptions: &autho.Options{Name: "a", Authenticator: auth},
			}
			h := attachRouteRateLimit(app, path, &bo.Options{}, nil)
			for range 2 {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
			}
			if auth.saw != tc.saw || !atApp {
				t.Fatalf("auth saw limit=%v app saw=%v", auth.saw, atApp)
			}
		})
	}
}

func TestPathNoneClearsLimiter(t *testing.T) {
	parent := &options.Options{Name: "parent", Limit: 1, Window: timeconv.Duration(time.Minute)}
	if err := parent.Validate(); err != nil {
		t.Fatal(err)
	}
	path := &po.Options{RateLimiterName: "none"}
	backend := &bo.Options{RateLimiterName: "parent", RateLimiter: parent}
	if routeLimiter(path, backend) != nil {
		t.Fatal("none inherited")
	}
	path.RateLimiterName = ""
	if routeLimiter(path, backend) != parent {
		t.Fatal("inherit")
	}
}

func limiterOptions(t *testing.T, name string, mutate func(*options.Options)) *options.Options {
	t.Helper()
	o := &options.Options{Name: name, Limit: 1, Window: timeconv.Duration(time.Minute)}
	if mutate != nil {
		mutate(o)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	return o
}

func bucketCount(name string) int {
	var n int
	ratelimit.Walk(func(got string, keys int) {
		if got == name {
			n = keys
		}
	})
	return n
}

func TestRouteACLDoesNotConsumeAndCountReachesLimiter(t *testing.T) {
	deny := mustIPList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}})
	limited := limiterOptions(t, "acl-deny-route", nil)
	path := &po.Options{
		Path: "/", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		IPACLName: "office", IPACL: deny,
		RateLimiterName: limited.Name, RateLimiter: limited,
	}
	h := applyMiddleware(&bo.Options{Name: "api", Provider: "prometheus"}, path, nil, nil, nil, routeLogging{}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.9:9"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || bucketCount("acl-deny-route") != 0 {
		t.Fatalf("deny status %d buckets %d", w.Code, bucketCount("acl-deny-route"))
	}

	counted := compiledGeo(t, "count-us", &geoaclopts.Options{Deny: []string{"US"}, Action: geoaclopts.ActionCount})
	routeLimit := limiterOptions(t, "acl-count-route", nil)
	var reached int
	path = &po.Options{
		Path: "/", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached++
			w.WriteHeader(http.StatusNoContent)
		}),
		GeoACLName: counted.Name, GeoACLOptions: counted,
		RateLimiterName: routeLimit.Name, RateLimiter: routeLimit,
	}
	h = applyMiddleware(&bo.Options{Name: "api", Provider: "prometheus"}, path, nil, nil, nil, routeLogging{}, nil, nil)
	for i := range 2 {
		req = httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.1:9"
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if i == 0 && w.Code != http.StatusNoContent {
			t.Fatalf("counted denial status %d", w.Code)
		}
	}
	if reached != 1 || bucketCount("acl-count-route") != 1 || w.Code != http.StatusTooManyRequests {
		t.Fatalf("reached %d buckets %d status %d", reached, bucketCount("acl-count-route"), w.Code)
	}
}

func TestRouteKeyIsStrippedAndPreRewrite(t *testing.T) {
	limited := limiterOptions(t, "strip-key", func(o *options.Options) { o.Keys = []string{"path"} })
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	path := &po.Options{Path: "/query", Handler: app, RateLimiterName: limited.Name, RateLimiter: limited}
	backend := &bo.Options{Name: "prom", Provider: "prometheus"}
	stripped := middleware.StripPathPrefix("/prom", applyMiddleware(backend, path, nil, nil, nil, routeLogging{}, nil, nil))
	raw := applyMiddleware(backend, path, nil, nil, nil, routeLogging{}, nil, nil)
	one := func(h http.Handler, path string) int {
		req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
		req.URL.Path = path
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if one(stripped, "/prom/query") != http.StatusNoContent {
		t.Fatal("stripped request")
	}
	if one(raw, "/query") != http.StatusTooManyRequests {
		t.Fatal("stripped key was not /query")
	}
	if one(raw, "/prom/query") != http.StatusNoContent {
		t.Fatal("full path shared the stripped key")
	}

	ri, err := rewriter.ParseRewriteList(rwopts.RewriteList{{"path", "set", "/rewritten"}})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	pre := limiterOptions(t, "pre-rewrite", func(o *options.Options) { o.Keys = []string{"path"} })
	path = &po.Options{
		Path: "/query", ReqRewriter: ri,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}),
		RateLimiterName: pre.Name, RateLimiter: pre,
	}
	h := applyMiddleware(backend, path, nil, nil, nil, routeLogging{}, nil, nil)
	if one(h, "/alpha") != http.StatusNoContent || seen != "/rewritten" {
		t.Fatalf("first %s", seen)
	}
	if one(h, "/beta") != http.StatusNoContent {
		t.Fatal("pre-rewrite paths shared a bucket")
	}
}

func TestALBRouteUsesBackendLimiter(t *testing.T) {
	limited := limiterOptions(t, "alb-own", nil)
	o := bo.New()
	o.Name = "pool"
	o.Provider = providers.ALB
	o.ALBOptions = &albopts.Options{MechanismName: names.MechanismRR}
	o.Paths = po.List{{
		Path: "/up", MatchType: matching.PathMatchTypeExact, Methods: []string{http.MethodGet},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		RateLimiterName: limited.Name, RateLimiter: limited,
	}}
	client, err := alb.NewClient("pool", o, lm.NewRouter(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	front := lm.NewRouter()
	RegisterPathRoutes(front, config.NewConfig(), nil, client, o, nil, nil)
	one := func() int {
		w := httptest.NewRecorder()
		front.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pool/up", nil))
		return w.Code
	}
	if one() != http.StatusNoContent {
		t.Fatal("alb route")
	}
	if code := one(); code != http.StatusTooManyRequests || bucketCount("alb-own") != 1 {
		t.Fatalf("alb limit %d buckets %d", code, bucketCount("alb-own"))
	}
}

func TestAccessLogAndRouteMetricsSee429(t *testing.T) {
	dir := t.TempDir()
	logOpts := &alo.Options{Filename: filepath.Join(dir, "access.log"), Format: "%s"}
	l, err := accesslog.NewLogger(logOpts, 0, "api", "prometheus")
	if err != nil {
		t.Fatal(err)
	}
	limited := limiterOptions(t, "logged-429", nil)
	path := &po.Options{
		Path: "/q", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		RateLimiterName: limited.Name, RateLimiter: limited,
	}
	backend := &bo.Options{Name: "api", Provider: "prometheus"}
	before := testutil.ToFloat64(metrics.FrontendRequestStatus.WithLabelValues("api", "prometheus", http.MethodGet, "/q", "4xx"))
	h := applyMiddleware(backend, path, nil, nil, nil, routeLogging{logger: l}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/q", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", w.Code)
	}
	if got := testutil.ToFloat64(metrics.FrontendRequestStatus.WithLabelValues("api", "prometheus", http.MethodGet, "/q", "4xx")); got != before+1 {
		t.Fatalf("4xx = %v", got)
	}
	l.Close()
	body, err := os.ReadFile(logOpts.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "429") {
		t.Fatalf("access log %q", body)
	}
}

func mustIPList(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	list, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func compiledGeo(t *testing.T, name string, o *geoaclopts.Options) *geoaclopts.Options {
	t.Helper()
	feed, err := geofeed.New(t.Name(), &geofeedopts.Options{Entries: []string{"192.0.2.1,US"}})
	if err != nil {
		t.Fatal(err)
	}
	o.Name = name
	a, err := geoacl.Compile(o, feed, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	o.Compiled = a
	return o
}
