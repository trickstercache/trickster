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
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxy"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/providers/basic"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize"
	pno "github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

func TestPathNormalizationGuardsUnauthenticatedPaths(t *testing.T) {
	// a path exempt from the backend's authenticator cannot reach another path: each
	// request is routed as the path its origin receives, or refused
	const user, password, originURIHeader = "client", "client-password", "X-Origin-URI"
	logger.SetLogger(logging.NoopLogger())
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(originURIHeader, r.RequestURI)
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	conf := config.NewConfig()
	o := bo.New()
	o.Provider, o.OriginURL = providers.ReverseProxy, origin.URL
	o.Hosts = []string{"example.com"}
	o.AuthOptions = &autho.Options{
		Name: "client-auth", Provider: basic.ID,
		Users: configtypes.EnvStringMap{user: password},
	}
	var err error
	if o.AuthOptions.Authenticator, err = basic.New(map[string]any{"options": o.AuthOptions}); err != nil {
		t.Fatal(err)
	}
	if err := o.Initialize("default"); err != nil {
		t.Fatal(err)
	}
	client, err := reverseproxy.NewClient("default", o, lm.NewRouter(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.HTTPClient = client.HTTPClient()
	public := po.New()
	public.Path, public.HandlerName = "/public/", providers.Proxy
	public.MatchType, public.MatchTypeName = matching.PathMatchTypePrefix, matching.PathMatchNamePrefix
	public.Methods = []string{http.MethodGet}
	public.AuthenticatorName = reserved.ReferenceNone
	if err := public.Initialize(""); err != nil {
		t.Fatal(err)
	}
	o.Paths = append(client.DefaultPathConfigs(o), public)
	rtr := lm.NewRouter()
	RegisterPathRoutes(rtr, conf, client.Handlers(), client, o, nil, nil)

	const (
		probeLiteral = "/public/../admin/x"
		probeEncoded = "/public/%2e%2e/admin/x"
		probeHidden  = "/public//..%2fadmin/x"
		escaped      = "/public/a%2Fb"
	)
	// status is for a request without credentials: 401 shows the backend's authenticator ran
	type result struct {
		status    int
		originURI string
	}
	refused := result{http.StatusBadRequest, ""}
	admin := result{http.StatusUnauthorized, "/admin/x"}
	tests := []struct {
		name string
		o    *pno.Options
		want map[string]result
	}{
		{"defaults", nil, map[string]result{
			probeLiteral: admin, probeEncoded: admin, probeHidden: refused,
			escaped: {http.StatusOK, escaped},
		}},
		{"dot_segments reject", &pno.Options{DotSegments: pno.DotSegmentsReject}, map[string]result{
			probeLiteral: refused, probeEncoded: refused, probeHidden: refused,
			escaped: {http.StatusOK, escaped},
		}},
		{"merge_slashes", &pno.Options{MergeSlashes: true}, map[string]result{
			probeLiteral: admin, probeEncoded: admin, probeHidden: refused,
		}},
		{"escaped_slashes reject", &pno.Options{EscapedSlashes: pno.EscapedSlashesReject}, map[string]result{
			probeLiteral: admin, probeEncoded: admin, probeHidden: refused, escaped: refused,
		}},
		// unescaped, the empty segment absorbs the "..", so the request stays under /public/ and so does the origin
		{"escaped_slashes unescape", &pno.Options{EscapedSlashes: pno.EscapedSlashesUnescape}, map[string]result{
			probeLiteral: admin, probeEncoded: admin, probeHidden: {http.StatusOK, "/public/admin/x"},
			escaped: {http.StatusOK, "/public/a/b"},
		}},
		// opted out, the origin receives the dot-segments the router ignored, as before normalization existed
		{"dot_segments off", &pno.Options{DotSegments: pno.DotSegmentsOff}, map[string]result{
			probeLiteral: {http.StatusOK, probeLiteral},
		}},
		{
			"escaped_slashes unescape and merge_slashes",
			&pno.Options{EscapedSlashes: pno.EscapedSlashesUnescape, MergeSlashes: true},
			map[string]result{
				probeHidden: admin,
			},
		},
	}
	for _, test := range tests {
		h := normalize.Middleware(test.o, rtr)
		for target, want := range test.want {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
			if w.Code != want.status {
				t.Errorf("%s %s: status %d; want %d", test.name, target, w.Code, want.status)
				continue
			}
			if want.status == http.StatusBadRequest {
				continue
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.SetBasicAuth(user, password)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if got := w.Header().Get(originURIHeader); w.Code != http.StatusOK || got != want.originURI {
				t.Errorf("%s %s: origin received %q (status %d); want %q", test.name, target, got, w.Code,
					want.originURI)
			}
		}
	}
}
