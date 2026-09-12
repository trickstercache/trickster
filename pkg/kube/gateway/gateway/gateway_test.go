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

package gateway

import (
	"encoding/base64"
	"flag"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	cacheregistry "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/compile"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	authregistry "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

var update = flag.Bool("update", false, "rewrite the golden overlay files")

// controllerName is what the fixture GatewayClasses name
const controllerName = kubecfg.DefaultGatewayClassControllerName

// decoder reads both core and Gateway API kinds
var decoder = func() runtime.Decoder {
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := gwapiv1.Install(s); err != nil {
		panic(err)
	}
	if err := gwapiv1a2.Install(s); err != nil {
		panic(err)
	}
	return serializer.NewCodecFactory(s).UniversalDeserializer()
}()

// cache is a Cache built from decoded fixture objects
type cache struct {
	classes    []*gwapiv1.GatewayClass
	gateways   []*gwapiv1.Gateway
	routes     []*gwapiv1.HTTPRoute
	grpcRoutes []*gwapiv1.GRPCRoute
	tcpRoutes  []*gwapiv1a2.TCPRoute
	tlsRoutes  []*gwapiv1a2.TLSRoute
	udpRoutes  []*gwapiv1a2.UDPRoute
	grants     []*gwapiv1.ReferenceGrant
	policies   []*gwapiv1.BackendTLSPolicy
	cachePols  []*cachepolicy.CachePolicy
	services   map[string]*corev1.Service
	secrets    map[string]*corev1.Secret
	configMaps map[string]*corev1.ConfigMap
	namespaces map[string]*corev1.Namespace
}

func newCache() *cache {
	return &cache{
		services:   make(map[string]*corev1.Service),
		secrets:    make(map[string]*corev1.Secret),
		configMaps: make(map[string]*corev1.ConfigMap),
		namespaces: make(map[string]*corev1.Namespace),
	}
}

func (c *cache) GatewayClasses() []*gwapiv1.GatewayClass    { return c.classes }
func (c *cache) Gateways() []*gwapiv1.Gateway               { return c.gateways }
func (c *cache) HTTPRoutes() []*gwapiv1.HTTPRoute           { return c.routes }
func (c *cache) GRPCRoutes() []*gwapiv1.GRPCRoute           { return c.grpcRoutes }
func (c *cache) TCPRoutes() []*gwapiv1a2.TCPRoute           { return c.tcpRoutes }
func (c *cache) TLSRoutes() []*gwapiv1a2.TLSRoute           { return c.tlsRoutes }
func (c *cache) UDPRoutes() []*gwapiv1a2.UDPRoute           { return c.udpRoutes }
func (c *cache) ReferenceGrants() []*gwapiv1.ReferenceGrant { return c.grants }
func (c *cache) BackendTLSPolicies() []*gwapiv1.BackendTLSPolicy {
	return c.policies
}
func (c *cache) Namespace(name string) *corev1.Namespace     { return c.namespaces[name] }
func (c *cache) Service(ns, name string) *corev1.Service     { return c.services[ns+"/"+name] }
func (c *cache) ConfigMap(ns, name string) *corev1.ConfigMap { return c.configMaps[ns+"/"+name] }

func (c *cache) Secret(ns, name string) *corev1.Secret {
	s := c.secrets[ns+"/"+name]
	if s == nil || s.Type != corev1.SecretTypeTLS {
		return nil
	}
	return s
}

// exists reports whether a policy target is among the fixture objects
func (c *cache) exists(kind, ns, name string) bool {
	switch kind {
	case cachepolicy.KindGateway:
		return slices.ContainsFunc(c.gateways, func(g *gwapiv1.Gateway) bool {
			return g.Namespace == ns && g.Name == name
		})
	case cachepolicy.KindHTTPRoute:
		return slices.ContainsFunc(c.routes, func(r *gwapiv1.HTTPRoute) bool {
			return r.Namespace == ns && r.Name == name
		})
	case cachepolicy.KindService:
		return c.services[ns+"/"+name] != nil
	}
	return false
}

// prometheusPaths stands in for the provider's own predefined paths
func prometheusPaths(provider string) po.List {
	if provider != "prometheus" {
		return nil
	}
	return po.List{
		{Path: "/api/v1/query_range", HandlerName: "query_range",
			MatchTypeName: matching.PathMatchNameExact, Methods: []string{"GET", "POST"},
			CacheKeyParams: []string{"query", "step", "stats"}},
		{Path: "/api/v1/query", HandlerName: "query", MatchTypeName: matching.PathMatchNameExact,
			Methods: []string{"GET", "POST"}, CacheKeyParams: []string{"query", "time"}},
		{Path: "/api/v1/series", HandlerName: "series", MatchTypeName: matching.PathMatchNameExact,
			Methods: []string{"GET", "POST"}, CacheKeyParams: []string{"match[]", "start", "end"}},
		{Path: "/", HandlerName: "proxy", MatchTypeName: matching.PathMatchNamePrefix,
			Methods: []string{"GET", "POST"}},
	}
}

// prometheusPathNames is what the policy index compares declared paths against
func prometheusPathNames(provider string) []string {
	var out []string
	for _, p := range prometheusPaths(provider) {
		out = append(out, p.Path)
	}
	return out
}

// policyIndex indexes the fixture's cache policies as the controller would
func policyIndex(c *cache) *cachepolicy.Index {
	if len(c.cachePols) == 0 {
		return nil
	}
	return cachepolicy.New(c.cachePols, cachepolicy.Config{
		Known: known(), Exists: c.exists, ProviderPaths: prometheusPathNames,
	})
}

func decodePolicy(t *testing.T, doc string) *cachepolicy.CachePolicy {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(doc), &m))
	p, err := cachepolicy.FromUnstructured(&unstructured.Unstructured{Object: m})
	require.NoError(t, err)
	return p
}

func load(t *testing.T, path string) *cache {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	c := newCache()
	for doc := range strings.SplitSeq(string(data), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		if strings.Contains(doc, "kind: "+cachepolicy.Kind) {
			// the resource is not in any scheme, so it is read the way the watcher reads it
			c.cachePols = append(c.cachePols, decodePolicy(t, doc))
			continue
		}
		obj, _, err := decoder.Decode([]byte(doc), nil, nil)
		require.NoError(t, err, "decoding %s", path)
		switch o := obj.(type) {
		case *gwapiv1.GatewayClass:
			c.classes = append(c.classes, o)
		case *gwapiv1.Gateway:
			c.gateways = append(c.gateways, o)
		case *gwapiv1.HTTPRoute:
			c.routes = append(c.routes, o)
		case *gwapiv1.GRPCRoute:
			c.grpcRoutes = append(c.grpcRoutes, o)
		case *gwapiv1a2.TCPRoute:
			c.tcpRoutes = append(c.tcpRoutes, o)
		case *gwapiv1a2.TLSRoute:
			c.tlsRoutes = append(c.tlsRoutes, o)
		case *gwapiv1a2.UDPRoute:
			c.udpRoutes = append(c.udpRoutes, o)
		case *gwapiv1.ReferenceGrant:
			c.grants = append(c.grants, o)
		case *gwapiv1.BackendTLSPolicy:
			c.policies = append(c.policies, o)
		case *corev1.Service:
			c.services[o.Namespace+"/"+o.Name] = o
		case *corev1.Secret:
			c.secrets[o.Namespace+"/"+o.Name] = o
		case *corev1.ConfigMap:
			c.configMaps[o.Namespace+"/"+o.Name] = o
		case *corev1.Namespace:
			c.namespaces[o.Name] = o
		default:
			t.Fatalf("fixture %s holds an unsupported kind %T", path, obj)
		}
	}
	return c
}

func options(t *testing.T, mutate ...func(*kubecfg.Options)) *kubecfg.Options {
	t.Helper()
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeService
	for _, m := range mutate {
		m(o)
	}
	o.Initialize()
	require.NoError(t, o.Validate())
	return o
}

func known() ir.ConfiguredNames {
	return ir.ConfiguredNames{
		Caches:         sets.New([]string{"objects"}),
		NegativeCaches: sets.New([]string{"api-errors"}),
		Tracers:        sets.New([]string{}),
		Rewriters:      sets.New([]string{}),
		Authenticators: sets.New([]string{"gateway-auth"}),
	}
}

func translateFixture(t *testing.T, name string,
	mutate ...func(*kubecfg.Options),
) (*ir.IR, []Problem, *kubecfg.Options) {
	t.Helper()
	o := options(t, mutate...)
	c := load(t, filepath.Join("testdata", name+".yaml"))
	idx := policyIndex(c)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: o, KnownNames: known,
		Policies: idx,
	})
	// the controller reports the index's problems alongside the translator's
	return model, append(problems, idx.Problems()...), o
}

func golden(t *testing.T, name string, mutate ...func(*kubecfg.Options)) []Problem {
	t.Helper()
	model, problems, o := translateFixture(t, name, mutate...)
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	path := filepath.Join("testdata", name+".golden.yaml")
	if *update {
		require.NoError(t, os.WriteFile(path, overlay.Data, 0o600))
		return problems
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run with -update to create the golden file")
	require.Equal(t, string(want), string(overlay.Data),
		"generated overlay differs from %s", path)
	return problems
}

func details(problems []Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.String())
	}
	return out
}

func containing(t *testing.T, problems []Problem, fragments ...string) {
	t.Helper()
	var hits int
	for _, p := range problems {
		s := p.String()
		all := true
		for _, f := range fragments {
			if !strings.Contains(s, f) {
				all = false
				break
			}
		}
		if all {
			hits++
		}
	}
	require.Equal(t, 1, hits, "problems mentioning %v: want one, got %d in %v",
		fragments, hits, details(problems))
}

func TestTranslateBasic(t *testing.T) {
	require.Empty(t, golden(t, "basic"))
	model, _, _ := translateFixture(t, "basic")
	require.Len(t, model.Listeners, 1)
	require.Equal(t, "Gateway/infra/gw/http", model.Listeners[0].Name)
	require.Equal(t, 80, model.Listeners[0].Port)
	require.False(t, model.Listeners[0].External)
	require.Len(t, model.Routes, 1)
	require.Equal(t, []string{"shop.example.com"}, model.Routes[0].Hostnames)
	require.Equal(t, []string{"Gateway/infra/gw/http"}, model.Routes[0].Listeners)
}

func TestTranslateHostnameIntersection(t *testing.T) {
	// Hostnames are the intersection of listener and route; listeners sharing a
	// port serve the route once for the union of what they admitted
	require.Empty(t, golden(t, "hostnames"))
	model, _, _ := translateFixture(t, "hostnames")
	byName := make(map[string]ir.Route)
	for _, r := range model.Routes {
		byName[r.Name] = r
	}
	// port 80 merges the wildcard and precise listeners: api.example.com is
	// admitted by both, shop and deep.shop by the wildcard, other.net by none
	var port80 []string
	for name, r := range byName {
		if strings.HasPrefix(name, "HTTPRoute/shop/web|Gateway/shop/gw/wild|") {
			port80 = append(port80, r.Hostnames[0])
			require.ElementsMatch(t, []string{"Gateway/shop/gw/wild", "Gateway/shop/gw/exact"},
				r.Listeners)
		}
	}
	require.ElementsMatch(t, []string{"api.example.com", "shop.example.com",
		"deep.shop.example.com"}, port80)
	// the listener with no hostname admits all four
	var port8080 []string
	for name, r := range byName {
		if strings.HasPrefix(name, "HTTPRoute/shop/web|Gateway/shop/gw/any|") {
			port8080 = append(port8080, r.Hostnames[0])
		}
	}
	require.ElementsMatch(t, []string{"api.example.com", "shop.example.com",
		"deep.shop.example.com", "other.net"}, port8080)
	// a wildcard route narrowed to a precise listener takes the listener's
	// hostname
	r, ok := byName["HTTPRoute/shop/wildcard|Gateway/shop/gw/narrow|docs.example.com"]
	require.True(t, ok, "routes: %v", slices.Sorted(mapsKeys(byName)))
	require.Equal(t, []string{"docs.example.com"}, r.Hostnames)
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestTranslateListenerMerging(t *testing.T) {
	// Two Gateways on one port merge, a hostname repeated across them included when they name one
	// certificate; a hostname repeated within a Gateway or a changed protocol conflicts, older winning
	problems := golden(t, "listeners")
	containing(t, problems, "Gateway/infra/newer", `listener "twice"`, "already served by Gateway/infra/newer")
	containing(t, problems, "Gateway/infra/newer", `listener "wrong-protocol"`, "already serves http")
	containing(t, problems, "Gateway/infra/newer", `listener "custom"`,
		`protocol "example.com/Custom" is not supported`)
	containing(t, problems, "Gateway/infra/newer", `listener "passthrough"`, `tls mode "Passthrough"`)
	require.Len(t, problems, 4, "%v", details(problems))

	model, _, _ := translateFixture(t, "listeners")
	names := make([]string, 0, len(model.Listeners))
	for _, l := range model.Listeners {
		names = append(names, l.Name)
	}
	require.ElementsMatch(t, []string{"Gateway/infra/older/http", "Gateway/infra/older/https",
		"Gateway/infra/newer/http", "Gateway/infra/newer/shared", "Gateway/infra/newer/clash"}, names)
	require.Len(t, model.Certs, 1)
	require.Equal(t, "infra/a-tls", model.Certs[0].Name)
}

func TestTranslateHTTPSHostnameAcrossGateways(t *testing.T) {
	// Gateways sharing a hostname on one HTTPS port merge only while they name one certificate;
	// the newer Gateway's differing one is refused, status says so, and naming the shared heals it
	model, report := translateReport(t, "https-hostnames")
	names := make([]string, 0, len(model.Listeners))
	for _, l := range model.Listeners {
		names = append(names, l.Name)
	}
	require.ElementsMatch(t, []string{"Gateway/infra/older/https", "Gateway/infra/same/https",
		"Gateway/infra/other/hostless"}, names)
	require.Len(t, model.Certs, 2, "both certificates reach the port's store")
	var other ir.GatewayStatus
	for _, g := range report.Gateways {
		if g.Source.Name == "other" {
			other = g
		}
	}
	require.Len(t, other.Listeners, 2)
	refused, kept := other.Listeners[0], other.Listeners[1]
	require.True(t, condOf(t, refused.Conditions, "Conflicted").Status)
	require.EqualValues(t, gwapiv1.ListenerReasonHostnameConflict,
		condOf(t, refused.Conditions, "Conflicted").Reason)
	require.False(t, condOf(t, refused.Conditions, "Accepted").Status)
	require.Contains(t, condOf(t, refused.Conditions, "Conflicted").Message, "Gateway/infra/older")
	require.True(t, condOf(t, kept.Conditions, "Accepted").Status)
	require.False(t, condOf(t, kept.Conditions, "Conflicted").Status)

	c := load(t, filepath.Join("testdata", "https-hostnames.yaml"))
	for _, g := range c.gateways {
		if g.Name == "other" {
			g.Spec.Listeners[0].TLS.CertificateRefs[0].Name = "a-tls"
		}
	}
	report, _ = reportOf(t, c)
	for _, g := range report.Gateways {
		for _, l := range g.Listeners {
			require.True(t, condOf(t, l.Conditions, "Accepted").Status, "%s/%s", g.Source.Name, l.Name)
		}
	}
}

func TestTranslatePrecedence(t *testing.T) {
	// Duplicates go to the older route; header and method matches on a plainly
	// claimed path compile to a chain and a method split
	problems := golden(t, "precedence")
	containing(t, problems, "HTTPRoute/shop/duplicate", "already served by HTTPRoute/shop/stable")
	require.Len(t, problems, 1, "a lost prefix is reported once, not once per half: %v",
		details(problems))

	rtr := serveGenerated(t, "precedence")
	tests := []struct {
		name, method string
		headers      []string
		want         string
	}{
		{"plain get", http.MethodGet, nil, "stable"},
		{"canary header", http.MethodGet, []string{"X-Canary", "true"}, "canary"},
		{"post goes to the method match", http.MethodPost, nil, "writer"},
		{"the method match outranks the header match on post", http.MethodPost,
			[]string{"X-Canary", "true"}, "writer"},
		{"below the prefix", http.MethodGet, nil, "stable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, body := request(rtr, test.method, "shop.example.com", "/api/orders",
				test.headers...)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, test.want, body)
		})
	}
	code, _ := request(rtr, http.MethodGet, "shop.example.com", "/elsewhere")
	require.Equal(t, http.StatusNotFound, code)
}

func TestTranslateWeights(t *testing.T) {
	// Weights become a round-robin ALB; weight 0 drops the reference, an
	// unresolvable one keeps its share and answers with an error
	problems := golden(t, "weights")
	containing(t, problems, "HTTPRoute/shop/split", "backendRef 3", "service shop/missing not found")
	containing(t, problems, "HTTPRoute/shop/split", "rule 1", "no backendRefs")
	containing(t, problems, "HTTPRoute/shop/split", "rule 2", "weight 0")
	require.Len(t, problems, 3, "%v", details(problems))

	model, _, _ := translateFixture(t, "weights")
	require.Len(t, model.Backends, 3)
	split := model.Backends[0]
	require.Len(t, split.Members, 3, "the zero-weight reference is dropped")
	require.Equal(t, 3, split.Members[0].Weight)
	require.Equal(t, 1, split.Members[1].Weight)
	require.True(t, split.Members[2].Invalid)
	require.Equal(t, 3, split.Members[2].RefIndex, "the invalid reference keeps its slot")

	rtr := serveGenerated(t, "weights")
	seen := map[string]int{}
	for range 20 {
		code, body := request(rtr, http.MethodGet, "any.example.com", "/x")
		if code == http.StatusInternalServerError {
			seen["error"]++
			continue
		}
		require.Equal(t, http.StatusOK, code)
		seen[body]++
	}
	require.Equal(t, 12, seen["stable"], "%v", seen)
	require.Equal(t, 4, seen["canary"], "%v", seen)
	require.Equal(t, 4, seen["error"], "the unresolved reference holds its share")
	code, _ := request(rtr, http.MethodGet, "any.example.com", "/empty")
	require.Equal(t, http.StatusInternalServerError, code)
	code, _ = request(rtr, http.MethodGet, "any.example.com", "/zeroed")
	require.Equal(t, http.StatusInternalServerError, code)
}

func TestTranslateReferenceGrants(t *testing.T) {
	// A cross-namespace Service or Secret reference is honored only where a
	// ReferenceGrant in the target namespace permits it
	problems := golden(t, "grants")
	containing(t, problems, "Gateway/infra/gw", "locked/forbidden-tls", "not permitted by any ReferenceGrant")
	containing(t, problems, "HTTPRoute/shop/web", "backends/private-svc", "not permitted by any ReferenceGrant")
	require.Len(t, problems, 2, "%v", details(problems))

	model, _, _ := translateFixture(t, "grants")
	require.Len(t, model.Certs, 1)
	require.Equal(t, "certs/shared-tls", model.Certs[0].Name)
	require.Equal(t, []string{"certs/shared-tls"}, model.Listeners[0].CertRefs)
	require.Len(t, model.Backends, 2)
	require.False(t, model.Backends[0].Members[0].Invalid)
	require.Equal(t, "backends", model.Backends[0].Members[0].Service.Namespace)
	require.True(t, model.Backends[1].Members[0].Invalid)
}

func TestTranslateClassParameters(t *testing.T) {
	// A GatewayClass's parameters ConfigMap overrides the configured defaults for
	// every route served through the class
	require.Empty(t, golden(t, "class-params"))
	model, _, _ := translateFixture(t, "class-params")
	require.Len(t, model.Policies, 1)
	p := model.Policies[0]
	require.Equal(t, "GatewayClass//trickster", p.Name)
	require.Equal(t, "objects", p.CacheName)
	require.EqualValues(t, 45000, p.TimeoutMS)
	require.Equal(t, "gateway-auth", p.AuthenticatorName)
	require.Equal(t, "probe", p.HealthMode)
	require.Equal(t, kubecfg.RoutingModeService, p.RoutingMode)
	require.Equal(t, p.Name, model.Routes[0].Rules[0].Policy)
}

func classWith(t *testing.T, edit func(*cache)) (*ir.IR, []Problem) {
	// classWith translates the class-params fixture with its ConfigMap edited
	t.Helper()
	c := load(t, filepath.Join("testdata", "class-params.yaml"))
	edit(c)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
		KnownNames: known,
	})
	return model, problems
}

func TestTranslateInvalidClassParametersRefuseService(t *testing.T) {
	// A class whose parameters cannot be honored serves nothing rather than
	// serving on weaker defaults, and every Gateway of it is told why
	tests := []struct {
		name   string
		edit   func(*cache)
		detail string
	}{
		{"unknown key", func(c *cache) {
			c.configMaps["infra/gateway-params"].Data["colour"] = "blue"
		}, `parameter "colour" rejected`},
		{"unknown authenticator", func(c *cache) {
			c.configMaps["infra/gateway-params"].Data[ParamAuthenticatorName] = "nope"
		}, `no authenticator named "nope"`},
		{"unknown rewriter", func(c *cache) {
			c.configMaps["infra/gateway-params"].Data[ParamReqRewriterName] = "nope"
		}, `no request rewriter named "nope"`},
		{"bad duration", func(c *cache) {
			c.configMaps["infra/gateway-params"].Data[ParamTimeout] = "45"
		}, `parameter "timeout" rejected`},
		{"ConfigMap absent", func(c *cache) {
			delete(c.configMaps, "infra/gateway-params")
		}, "ConfigMap infra/gateway-params not found"},
		{"no namespace", func(c *cache) {
			c.classes[0].Spec.ParametersRef.Namespace = nil
		}, "names no namespace"},
		{"wrong kind", func(c *cache) {
			c.classes[0].Spec.ParametersRef.Kind = "Secret"
		}, "kind /Secret is not supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, problems := classWith(t, test.edit)
			containing(t, problems, "GatewayClass//trickster", test.detail)
			containing(t, problems, "Gateway/infra/gw", "is not accepted", "not served")
			require.Empty(t, model.Listeners, "an unaccepted class opens no port")
			require.Empty(t, model.Routes, "and serves no route")
			require.Empty(t, model.Policies)
		})
	}
}

func TestTranslateClassParametersLostAfterServing(t *testing.T) {
	// A class that was serving stops the moment its parameters can no longer be
	// honored; nothing is carried over from the earlier pass
	c := load(t, filepath.Join("testdata", "class-params.yaml"))
	cfg := Config{Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
		KnownNames: known}
	model, _, problems := Translate(cfg)
	require.Empty(t, problems)
	require.Len(t, model.Routes, 1)
	require.Equal(t, "gateway-auth", model.Policies[0].AuthenticatorName)

	delete(c.configMaps, "infra/gateway-params")
	model, _, problems = Translate(cfg)
	containing(t, problems, "ConfigMap infra/gateway-params not found")
	require.Empty(t, model.Routes)
	require.Empty(t, model.Listeners)

	// an empty ConfigMap is a class with no overrides, which is served
	c.configMaps["infra/gateway-params"] = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "gateway-params"}}
	model, _, problems = Translate(cfg)
	require.Empty(t, problems)
	require.Len(t, model.Routes, 1)
	require.Empty(t, model.Policies)
	require.Empty(t, model.Routes[0].Rules[0].Policy)
}

func TestTranslateAllowedRoutes(t *testing.T) {
	// allowedRoutes decides which namespaces may attach, and which of the served kinds a
	// listener admits: one admitting only GRPCRoute takes no HTTPRoute
	problems := golden(t, "allowed")
	containing(t, problems, "HTTPRoute/tenant-b/b", "no listener allows routes from namespace")
	require.Len(t, problems, 1, "%v", details(problems))

	model, report := translateReport(t, "allowed")
	require.Len(t, model.Routes, 1)
	require.Equal(t, "HTTPRoute/tenant-a/a", model.Routes[0].Source.Key())
	require.Equal(t, []string{"Gateway/infra/gw/selected"}, model.Routes[0].Listeners)
	for _, l := range report.Gateways[0].Listeners {
		if l.Name == "grpc-only" {
			require.Equal(t, []string{"GRPCRoute"}, l.SupportedKinds)
		}
	}

	// a listener admitting a kind this controller does not serve admits nothing
	c := load(t, filepath.Join("testdata", "allowed.yaml"))
	kind := gwapiv1.Kind("TCPRoute")
	c.gateways[0].Spec.Listeners[len(c.gateways[0].Spec.Listeners)-1].AllowedRoutes.Kinds =
		[]gwapiv1.RouteGroupKind{{Kind: kind}}
	_, report, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "Gateway/infra/gw", `listener "grpc-only"`, "TCPRoute is not supported")
	containing(t, problems, "Gateway/infra/gw", `listener "grpc-only"`, "admits no kind")
	for _, l := range report.Gateways[0].Listeners {
		if l.Name == "grpc-only" {
			require.Empty(t, l.SupportedKinds)
			require.False(t, condOf(t, l.Conditions, "ResolvedRefs").Status)
		}
	}
}

func TestGeneratedOverlayLoadsAndValidates(t *testing.T) {
	// Every fixture's overlay must survive the real loader, as the daemon's
	// reload would apply it
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(baseConfig), 0o600))
	matches, err := filepath.Glob(filepath.Join("testdata", "*.golden.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, matches)
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".golden.yaml")
		t.Run(name, func(t *testing.T) {
			model, _, o := translateFixture(t, name)
			overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
			require.NoError(t, err)
			conf, err := config.LoadWithOverlay([]string{"-config", path}, overlay)
			require.NoError(t, err)
			require.NoError(t, conf.Backends.Validate())
			require.NoError(t, validate.Validate(conf))
		})
	}
}

// baseConfig is the file configuration the generated overlay is merged
// onto; it defines what the fixtures' parameters name
const baseConfig = `
backends:
  default:
    provider: rp
    origin_url: http://example.com
caches:
  objects:
    provider: memory
negative_caches:
  api-errors:
    "404": 30s
authenticators:
  gateway-auth:
    provider: basic
    users:
      admin: $2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy
`

func TestTranslateIgnoresUnclaimed(t *testing.T) {
	// A cluster with no claimed class, or a Gateway of another class, translates
	// to nothing at all rather than to an empty listener set
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.classes[0].Spec.ControllerName = "someone.else/controller"
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.True(t, model.IsEmpty())
	require.Empty(t, problems)

	c = load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.GatewayClassName = "other"
	model, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.Empty(t, model.Listeners)
	require.Empty(t, model.Routes, "a route to another controller's Gateway is not ours")
	require.Empty(t, problems, "and is not this controller's to complain about")
}

func TestTranslateNilConfig(t *testing.T) {
	model, _, problems := Translate(Config{})
	require.True(t, model.IsEmpty())
	require.Empty(t, problems)
}

func mutateRoute(t *testing.T, edit func(*gwapiv1.HTTPRoute)) (*ir.IR, []Problem) {
	// mutateRoute translates the basic fixture with its route edited
	t.Helper()
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	edit(c.routes[0])
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	return model, problems
}

func TestTranslateUnsupportedRouteFeatures(t *testing.T) {
	// A filter this build cannot honor changes what a route means, so the route is not served;
	// a mirror whose Service does not resolve is dropped with ResolvedRefs lowered, as the API
	// defines, and the additive settings are only reported
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
			Type:          gwapiv1.HTTPRouteFilterRequestMirror,
			RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{}}}
	})
	containing(t, problems, "requestMirror: service shop/: no port named; the mirror is not applied")
	require.Len(t, model.Routes, 1)
	require.Empty(t, model.Routes[0].Rules[0].Filters)

	model, problems = mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules[0].BackendRefs[0].Filters = []gwapiv1.HTTPRouteFilter{{
			Type: gwapiv1.HTTPRouteFilterExtensionRef}}
	})
	containing(t, problems, "backendRef 0: filter 0 (ExtensionRef): filter type is not supported")
	require.Empty(t, model.Routes)

	d := gwapiv1.Duration("5s")
	model, problems = mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules[0].Timeouts = &gwapiv1.HTTPRouteTimeouts{Request: &d}
		hr.Spec.Rules[0].Retry = &gwapiv1.HTTPRouteRetry{}
		hr.Spec.Rules[0].SessionPersistence = &gwapiv1.SessionPersistence{}
	})
	containing(t, problems, "sessionPersistence is not supported and is ignored")
	require.Len(t, model.Routes, 1)
	require.Equal(t, &ir.RuleTimeouts{RequestMS: 5000}, model.Routes[0].Rules[0].Timeouts)
	require.Equal(t, &ir.RuleRetry{Attempts: 1}, model.Routes[0].Rules[0].Retry)
	require.Len(t, model.Routes, 1, "the route is still served")
}

func TestTranslateFilterRejections(t *testing.T) {
	// Every way a filter can be malformed fails the whole route with a reason naming the
	// filter, so a route is never served under a filter that means something else
	str := func(s string) *string { return &s }
	host := gwapiv1.PreciseHostname("x.example.com")
	wild := gwapiv1.PreciseHostname("*.example.com")
	port := gwapiv1.PortNumber(0)
	code := 300
	full := gwapiv1.FullPathHTTPPathModifier
	prefix := gwapiv1.PrefixMatchHTTPPathModifier
	exact := gwapiv1.PathMatchExact
	weight := int32(1)
	headerMod := func(name, value string) *gwapiv1.HTTPHeaderFilter {
		return &gwapiv1.HTTPHeaderFilter{Set: []gwapiv1.HTTPHeader{{
			Name: gwapiv1.HTTPHeaderName(name), Value: value}}}
	}
	tests := []struct {
		name string
		edit func(*gwapiv1.HTTPRoute)
		want string
	}{
		{"no body", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier}}
		}, "carries no configuration"},
		{"repeated", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{
				{Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
					RequestHeaderModifier: headerMod("X-A", "1")},
				{Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
					RequestHeaderModifier: headerMod("X-B", "2")}}
		}, "may not be repeated"},
		{"redirect and rewrite", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{
				{Type: gwapiv1.HTTPRouteFilterRequestRedirect,
					RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: str("https")}},
				{Type: gwapiv1.HTTPRouteFilterURLRewrite,
					URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Hostname: &host}}}
		}, "cannot both be used"},
		{"redirect on backendRef", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
				RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: str("https")}}}
		}, "not permitted on a backendRef"},
		{"bad header name", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:                  gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: headerMod("X A", "1")}}
		}, "not a valid header name"},
		{"bad header value", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:                   gwapiv1.HTTPRouteFilterResponseHeaderModifier,
				ResponseHeaderModifier: headerMod("X-A", "bad\x00value")}}
		}, "not a valid header value"},
		{"bad remove", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:                   gwapiv1.HTTPRouteFilterResponseHeaderModifier,
				ResponseHeaderModifier: &gwapiv1.HTTPHeaderFilter{Remove: []string{"X A"}}}}
		}, "not a valid header name"},
		{"bad scheme", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
				RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: str("ftp")}}}
		}, "scheme must be http or https"},
		{"wildcard redirect hostname", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
				RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Hostname: &wild}}}
		}, "hostname must be precise"},
		{"bad port", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
				RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Port: &port}}}
		}, "port must be between"},
		{"bad status", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
				RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{StatusCode: &code}}}
		}, "statusCode must be 301, 302, 303, 307 or 308"},
		{"relative full path", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: full, ReplaceFullPath: str("v2")}}}}
		}, "must begin with '/'"},
		{"relative prefix", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: prefix, ReplacePrefixMatch: str("v2")}}}}
		}, "must begin with '/'"},
		{"prefix replacement on an exact match", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Matches[0].Path.Type = &exact
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: prefix, ReplacePrefixMatch: str("/v2")}}}}
		}, "requires exactly one PathPrefix match"},
		{"prefix replacement on two matches", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Matches = append(hr.Spec.Rules[0].Matches,
				gwapiv1.HTTPRouteMatch{})
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: prefix, ReplacePrefixMatch: str("/v2")}}}}
		}, "requires exactly one PathPrefix match"},
		{"prefix replacement behind a weighted rule", func(hr *gwapiv1.HTTPRoute) {
			second := hr.Spec.Rules[0].BackendRefs[0]
			second.Weight = &weight
			second.Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: prefix, ReplacePrefixMatch: str("/v2")}}}}
			hr.Spec.Rules[0].BackendRefs = append(hr.Spec.Rules[0].BackendRefs, second)
		}, "requires a rule with one backendRef"},
		{"path rewritten at both levels", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: full, ReplaceFullPath: str("/a")}}}}
			hr.Spec.Rules[0].BackendRefs[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: full, ReplaceFullPath: str("/b")}}}}
		}, "both rewrite the path"},
		{"header repeated in set", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{Set: []gwapiv1.HTTPHeader{
					{Name: "X-A", Value: "1"}, {Name: "X-A", Value: "2"}}}}}
		}, "named once per filter"},
		{"header repeated in set by case", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{Set: []gwapiv1.HTTPHeader{
					{Name: "Authorization", Value: "1"}, {Name: "authorization", Value: "2"}}}}}
		}, "named once per filter"},
		{"header set and added", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
				RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{
					Set: []gwapiv1.HTTPHeader{{Name: "X-A", Value: "1"}},
					Add: []gwapiv1.HTTPHeader{{Name: "x-a", Value: "2"}}}}}
		}, "named once per filter"},
		{"header set and removed", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterResponseHeaderModifier,
				ResponseHeaderModifier: &gwapiv1.HTTPHeaderFilter{
					Set:    []gwapiv1.HTTPHeader{{Name: "Authorization", Value: "1"}},
					Remove: []string{"authorization"}}}}
		}, "named once per filter"},
		{"header added and removed", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterResponseHeaderModifier,
				ResponseHeaderModifier: &gwapiv1.HTTPHeaderFilter{
					Add:    []gwapiv1.HTTPHeader{{Name: "Vary", Value: "1"}},
					Remove: []string{"VARY"}}}}
		}, "named once per filter"},
		{"location set on a redirect", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{
				{Type: gwapiv1.HTTPRouteFilterRequestRedirect,
					RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: str("https")}},
				{Type: gwapiv1.HTTPRouteFilterResponseHeaderModifier,
					ResponseHeaderModifier: headerMod("Location", "https://elsewhere.example.com/")}}
		}, "may not modify Location"},
		{"location added on a redirect, lowercase", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{
				{Type: gwapiv1.HTTPRouteFilterResponseHeaderModifier,
					ResponseHeaderModifier: &gwapiv1.HTTPHeaderFilter{
						Add: []gwapiv1.HTTPHeader{{Name: "location", Value: "/x"}}}},
				{Type: gwapiv1.HTTPRouteFilterRequestRedirect,
					RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: str("https")}}}
		}, "may not modify Location"},
		{"location removed on a redirect", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].BackendRefs = nil
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{
				{Type: gwapiv1.HTTPRouteFilterRequestRedirect,
					RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: str("https")}},
				{Type: gwapiv1.HTTPRouteFilterResponseHeaderModifier,
					ResponseHeaderModifier: &gwapiv1.HTTPHeaderFilter{Remove: []string{"LOCATION"}}}}
		}, "may not modify Location"},
		{"unknown path modifier", func(hr *gwapiv1.HTTPRoute) {
			hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
				Type: gwapiv1.HTTPRouteFilterURLRewrite,
				URLRewrite: &gwapiv1.HTTPURLRewriteFilter{Path: &gwapiv1.HTTPPathModifier{
					Type: "Regex"}}}}
		}, "unsupported path modifier type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, problems := mutateRoute(t, tt.edit)
			containing(t, problems, tt.want, "the route is not served")
			require.Empty(t, model.Routes)
		})
	}
}

func TestTranslateFilterRejectionIsScopedToTheRoute(t *testing.T) {
	// A rejected filter takes down its own route and no other: the sibling
	// HTTPRoutes on the same Gateway keep serving
	c := load(t, filepath.Join("testdata", "filters.yaml"))
	for _, hr := range c.routes {
		if hr.Name == "headers" {
			// the fixture's own request header modifier, made invalid
			hr.Spec.Rules[0].Filters[0].RequestHeaderModifier = &gwapiv1.HTTPHeaderFilter{
				Set:    []gwapiv1.HTTPHeader{{Name: "Authorization", Value: "x"}},
				Remove: []string{"authorization"},
			}
		}
	}
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t), KnownNames: known,
	})
	containing(t, problems, "HTTPRoute/shop/headers", "named once per filter", "the route is not served")
	var served []string
	for _, r := range model.Routes {
		served = append(served, r.Source.Name)
	}
	require.NotContains(t, served, "headers")
	require.Contains(t, served, "moved")
	require.Contains(t, served, "split")
}

func TestTranslateLocationModifierOnForwardingRule(t *testing.T) {
	// A Location response-header modification on a forwarding rule is ordinary;
	// only a redirecting rule owns its Location
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
			Type:                   gwapiv1.HTTPRouteFilterResponseHeaderModifier,
			ResponseHeaderModifier: &gwapiv1.HTTPHeaderFilter{Remove: []string{"Location"}}}}
	})
	require.Empty(t, problems)
	require.Len(t, model.Routes, 1)
}

func TestRequestHeaderFilterSetsHostInAnyCase(t *testing.T) {
	// The upstream Host is r.Host on the wire, so a Host update under any
	// spelling of the name has to reach it through the real engine
	for _, name := range []string{"Host", "HOST", "hOsT"} {
		t.Run(name, func(t *testing.T) {
			c := load(t, filepath.Join("testdata", "filters.yaml"))
			for _, hr := range c.routes {
				if hr.Name != "headers" {
					continue
				}
				hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
					Type: gwapiv1.HTTPRouteFilterRequestHeaderModifier,
					RequestHeaderModifier: &gwapiv1.HTTPHeaderFilter{Set: []gwapiv1.HTTPHeader{
						{Name: gwapiv1.HTTPHeaderName(name), Value: "tenant.internal"}}}}}
			}
			o := options(t)
			model, _, problems := Translate(Config{
				Cache: c, Claimer: class.New(controllerName, ""), Options: o, KnownNames: known,
			})
			onlyTheDeadFilter(t, problems)
			overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
			require.NoError(t, err)
			var got struct {
				mtx  sync.Mutex
				host string
			}
			rtr := serveOverlayWith(t, overlay, func(w http.ResponseWriter, r *http.Request) {
				got.mtx.Lock()
				got.host = r.Host
				got.mtx.Unlock()
				w.WriteHeader(http.StatusOK)
			})
			status, _, _ := requestFull(rtr, http.MethodGet, "shop.example.com", "/api/x")
			require.Equal(t, http.StatusOK, status)
			got.mtx.Lock()
			defer got.mtx.Unlock()
			require.Equal(t, "tenant.internal", got.host, "the upstream must see the configured Host")
		})
	}
}

func TestTranslateRedirectIgnoresBackendRefs(t *testing.T) {
	// A redirecting rule dispatches nowhere; backendRefs written on one are
	// ignored and said so
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		https := "https"
		hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
			Type:            gwapiv1.HTTPRouteFilterRequestRedirect,
			RequestRedirect: &gwapiv1.HTTPRequestRedirectFilter{Scheme: &https}}}
	})
	containing(t, problems, "backendRefs are ignored on a redirecting rule")
	require.Len(t, model.Routes, 1)
	require.Len(t, model.Backends, 1)
	require.Empty(t, model.Backends[0].Members)
	require.NotNil(t, model.Routes[0].Rules[0].Redirect())
}

func TestTranslateFilters(t *testing.T) {
	onlyTheDeadFilter(t, golden(t, "filters"))
}

func onlyTheDeadFilter(t *testing.T, problems []Problem) {
	// onlyTheDeadFilter asserts the one problem the filters fixture carries by
	// design: the request header modifier it declares after a redirect
	t.Helper()
	containing(t, problems, "HTTPRoute/shop/tenant",
		"rule 1: filter 1 (RequestHeaderModifier) follows the RequestRedirect")
	require.Len(t, problems, 1, "%v", details(problems))
}

func TestRedirectHonorsRequestHeadersDeclaredBeforeIt(t *testing.T) {
	// A request header modifier ahead of a redirect shapes the Location's hostname, one after it
	// has no request left to modify, and a forwarding rule applies its headers exactly once
	var got struct {
		mtx   sync.Mutex
		trace []string
	}
	rtr := serveGeneratedWith(t, "filters", func(w http.ResponseWriter, r *http.Request) {
		got.mtx.Lock()
		got.trace = r.Header.Values("X-Trace")
		got.mtx.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	status, _, hdr := requestFull(rtr, http.MethodGet, "shop.example.com:8080", "/tenant/x")
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, "https://tenant.example.com/tenant/x", hdr.Get("Location"),
		"the Host set ahead of the redirect is the Location's hostname")

	status, _, hdr = requestFull(rtr, http.MethodGet, "shop.example.com", "/late/x")
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, "https://shop.example.com/late/x", hdr.Get("Location"),
		"a Host set after the redirect modifies nothing the redirect answers")

	status, _, _ = requestFull(rtr, http.MethodGet, "shop.example.com", "/api/x",
		"X-Trace", "start")
	require.Equal(t, http.StatusOK, status)
	got.mtx.Lock()
	defer got.mtx.Unlock()
	require.Equal(t, []string{"start", "traced"}, got.trace,
		"a forwarding rule applies its request headers once")
}

func TestFiltersAreServed(t *testing.T) {
	// Filters are served through the real router: request and response headers are modified,
	// the path and Host are rewritten, and a redirecting rule answers with the composed Location
	var got struct {
		mtx     sync.Mutex
		path    string
		host    string
		headers http.Header
	}
	rtr := serveGeneratedWith(t, "filters", func(w http.ResponseWriter, r *http.Request) {
		got.mtx.Lock()
		got.path, got.host, got.headers = r.URL.Path, r.Host, r.Header.Clone()
		got.mtx.Unlock()
		w.Header().Set("Server", "origin")
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write([]byte("ok"))
	})

	status, body, hdr := requestFull(rtr, http.MethodGet, "shop.example.com", "/api/orders/1",
		"X-Internal", "secret", "X-Trace", "start")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", body)
	got.mtx.Lock()
	require.Equal(t, "/v2/orders/1", got.path, "the matched prefix is replaced")
	require.Equal(t, "web.internal", got.host, "the Host header is rewritten")
	require.Equal(t, "prod", got.headers.Get("X-Env"))
	require.Equal(t, []string{"start", "traced"}, got.headers.Values("X-Trace"),
		"add appends to the request's value")
	require.Empty(t, got.headers.Get("X-Internal"), "remove deletes the request's header")
	got.mtx.Unlock()
	require.Equal(t, "no-store", hdr.Get("Cache-Control"))
	require.Equal(t, []string{"Accept-Encoding", "Accept"}, hdr.Values("Vary"))
	require.Empty(t, hdr.Get("Server"))

	status, _, hdr = requestFull(rtr, http.MethodGet, "shop.example.com", "/old/thing?x=1")
	require.Equal(t, http.StatusMovedPermanently, status)
	require.Equal(t, "https://new.example.com:8443/new?x=1", hdr.Get("Location"))
	require.Equal(t, "no-store", hdr.Get("Cache-Control"),
		"a response-header filter applies to the redirection itself")

	status, _, hdr = requestFull(rtr, http.MethodGet, "shop.example.com:8080", "/login")
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, "https://shop.example.com/login", hdr.Get("Location"),
		"an explicit scheme drops the request port")

	// the exact half of a prefix match is rewritten too
	status, _, _ = requestFull(rtr, http.MethodGet, "shop.example.com", "/api")
	require.Equal(t, http.StatusOK, status)
	got.mtx.Lock()
	require.Equal(t, "/v2", got.path)
	got.mtx.Unlock()
}

func TestRedirectAnswersUpgradeRequests(t *testing.T) {
	// A redirecting rule forwards nothing, not even a request asking to upgrade the
	// connection: it is answered with the redirection and no upstream is dialed
	var reached atomic.Int32
	rtr := serveGeneratedWith(t, "filters", func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	upgrade := []string{"Connection", "Upgrade", "Upgrade", "websocket"}
	status, _, hdr := requestFull(rtr, http.MethodGet, "shop.example.com", "/login",
		upgrade...)
	require.Equal(t, http.StatusFound, status, "a scheme-only redirect answers the handshake")
	require.Equal(t, "https://shop.example.com/login", hdr.Get("Location"))
	status, _, hdr = requestFull(rtr, http.MethodGet, "shop.example.com", "/old/ws",
		upgrade...)
	require.Equal(t, http.StatusMovedPermanently, status,
		"a redirect naming a hostname must not be proxied to that hostname")
	require.Equal(t, "https://new.example.com:8443/new", hdr.Get("Location"))
	require.Zero(t, reached.Load(), "no upstream is reached for a redirecting rule")

	// a forwarding rule still tunnels upgrades to its origin
	status, _, _ = requestFull(rtr, http.MethodGet, "shop.example.com", "/api/ws", upgrade...)
	require.NotEqual(t, http.StatusFound, status)
}

func TestRedirectUsesTheListenerPort(t *testing.T) {
	// A redirect naming neither scheme nor port sends the client back to the port it arrived
	// on, whatever the Host header carried, and a route on two listeners does so once per port
	model, _, o := translateFixture(t, "filters")
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	routers := serveOverlayOn(t, overlay, baseConfig, nil)
	alt, std := routers[compile.ListenerName(8080, ir.ProtocolHTTP)],
		routers[compile.ListenerName(80, ir.ProtocolHTTP)]
	require.NotNil(t, alt)
	require.NotNil(t, std)

	for _, host := range []string{"shop.example.com", "shop.example.com:9999", "shop.example.com:80"} {
		status, _, hdr := requestFull(alt, http.MethodGet, host, "/alt/x")
		require.Equal(t, http.StatusFound, status, host)
		require.Equal(t, "http://new.example.com:8080/alt/x", hdr.Get("Location"),
			"the listener's port, not the Host header's (%s)", host)
	}
	status, _, hdr := requestFull(std, http.MethodGet, "both.example.com:9999", "/both")
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, "http://new.example.com/both", hdr.Get("Location"),
		"the well-known port of the listener's scheme is omitted")
	status, _, hdr = requestFull(alt, http.MethodGet, "both.example.com", "/both")
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, "http://new.example.com:8080/both", hdr.Get("Location"))
	status, _, _ = requestFull(std, http.MethodGet, "shop.example.com", "/alt/x")
	require.Equal(t, http.StatusNotFound, status, "the route is attached to one listener only")
}

func TestRedirectIsAuthenticated(t *testing.T) {
	// The operator's authenticator guards a redirecting rule exactly as it guards
	// a forwarding one, from the defaults and from a GatewayClass's parameters
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	require.NoError(t, err)
	base := strings.Replace(baseConfig,
		"$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", string(hash), 1)
	require.NotEqual(t, baseConfig, base)

	check := func(t *testing.T, rtr router.Router) {
		t.Helper()
		status, _, hdr := requestFull(rtr, http.MethodGet, "shop.example.com", "/login")
		require.Equal(t, http.StatusUnauthorized, status, "no credentials")
		require.Empty(t, hdr.Get("Location"), "an unauthenticated request learns nothing")
		status, _, _ = requestFull(rtr, http.MethodGet, "shop.example.com", "/login",
			"Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:wrong")))
		require.Equal(t, http.StatusUnauthorized, status, "wrong credentials")
		status, _, hdr = requestFull(rtr, http.MethodGet, "shop.example.com", "/login",
			"Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:s3cret")))
		require.Equal(t, http.StatusFound, status, "valid credentials receive the redirect")
		require.Equal(t, "https://shop.example.com/login", hdr.Get("Location"))
	}

	t.Run("defaults", func(t *testing.T) {
		model, _, o := translateFixture(t, "filters", func(o *kubecfg.Options) {
			o.Defaults.AuthenticatorName = "gateway-auth"
		})
		overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
		require.NoError(t, err)
		check(t, serveOverlayOn(t, overlay, base, nil)[""])
	})
	t.Run("class parameters", func(t *testing.T) {
		c := load(t, filepath.Join("testdata", "filters.yaml"))
		params := "infra/params"
		c.configMaps[params] = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "infra", Name: "params"},
			Data:       map[string]string{ParamAuthenticatorName: "gateway-auth"},
		}
		c.classes[0].Spec.ParametersRef = &gwapiv1.ParametersReference{
			Group: "", Kind: "ConfigMap", Name: "params",
			Namespace: func() *gwapiv1.Namespace { n := gwapiv1.Namespace("infra"); return &n }(),
		}
		o := options(t)
		model, _, problems := Translate(Config{
			Cache: c, Claimer: class.New(controllerName, ""), Options: o, KnownNames: known,
		})
		onlyTheDeadFilter(t, problems)
		overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
		require.NoError(t, err)
		check(t, serveOverlayOn(t, overlay, base, nil)[""])
	})
}

func TestBackendRefFiltersFollowTheMember(t *testing.T) {
	// backendRef filters apply only to the member they are written on
	var got struct {
		mtx      sync.Mutex
		variants map[string]int
		paths    map[string]int
	}
	got.variants, got.paths = map[string]int{}, map[string]int{}
	rtr := serveGeneratedWith(t, "filters", func(w http.ResponseWriter, r *http.Request) {
		got.mtx.Lock()
		got.variants[r.Header.Get("X-Variant")]++
		got.paths[r.URL.Path]++
		got.mtx.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	var vary []string
	for range 8 {
		status, _, hdr := requestFull(rtr, http.MethodGet, "shop.example.com", "/split/x")
		require.Equal(t, http.StatusOK, status)
		vary = append(vary, hdr.Get("Vary"))
	}
	got.mtx.Lock()
	defer got.mtx.Unlock()
	require.Equal(t, 6, got.variants["stable"], "weights 3:1 over 8 requests")
	require.Equal(t, 2, got.variants["canary"])
	require.Equal(t, 6, got.paths["/split/x"], "the stable member sees the request path")
	require.Equal(t, 2, got.paths["/canary"], "the canary member's full-path rewrite applies to it alone")
	require.Equal(t, 2, countOf(vary, "X-Variant"), "the canary's response header follows its responses")
}

func countOf(in []string, v string) int {
	var n int
	for _, s := range in {
		if s == v {
			n++
		}
	}
	return n
}

func TestTranslateBackendTLS(t *testing.T) {
	problems := golden(t, "backend-tls")
	containing(t, problems, "BackendTLSPolicy/shop/tls-system", "options are not supported")
	containing(t, problems, "BackendTLSPolicy/shop/tls-broken", "caCertificateRef not found")
	containing(t, problems, "BackendTLSPolicy/shop/tls-late", "already selected by the older policy")
	require.Len(t, problems, 3, "%v", details(problems))

	model, _, _ := translateFixture(t, "backend-tls")
	byService := make(map[string]ir.BackendMember)
	for _, g := range model.Backends {
		for _, m := range g.Members {
			byService[m.Service.Name] = m
		}
	}
	secure := byService["secure-svc"]
	require.Equal(t, ir.ProtocolHTTPS, secure.Service.Scheme)
	require.NotNil(t, secure.TLS)
	require.Equal(t, "secure.internal", secure.TLS.Hostname)
	require.Len(t, secure.TLS.CACertificates, 1)
	require.False(t, secure.TLS.System)
	system := byService["system-svc"]
	require.Equal(t, ir.ProtocolHTTPS, system.Service.Scheme)
	require.True(t, system.TLS.System)
	require.Empty(t, system.TLS.CACertificates)
	for _, g := range model.Backends {
		for _, m := range g.Members {
			if m.Invalid {
				require.Contains(t, m.InvalidReason, "tls-broken",
					"a policy that cannot be honored fails the reference rather than "+
						"connecting unverified")
			}
		}
	}
}

func TestTranslateBackendTLSRejections(t *testing.T) {
	// Every way a BackendTLSPolicy can be malformed makes the references it
	// governs invalid, with a reason naming the policy
	str := func(s string) *string { return &s }
	tests := []struct {
		name string
		edit func(*gwapiv1.BackendTLSPolicy)
		want string
	}{
		{"wildcard hostname", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.Hostname = "*.internal"
		}, "hostname must be precise"},
		{"subject alt names", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.SubjectAltNames = []gwapiv1.SubjectAltName{{
				Type: gwapiv1.HostnameSubjectAltNameType, Hostname: "a.internal"}}
		}, "subjectAltNames are not supported"},
		{"both validations", func(p *gwapiv1.BackendTLSPolicy) {
			wk := gwapiv1.WellKnownCACertificatesType("System")
			p.Spec.Validation.WellKnownCACertificates = &wk
		}, "names both"},
		{"no validation", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs = nil
		}, "names neither"},
		{"unknown well-known set", func(p *gwapiv1.BackendTLSPolicy) {
			wk := gwapiv1.WellKnownCACertificatesType("example.com/mine")
			p.Spec.Validation.CACertificateRefs = nil
			p.Spec.Validation.WellKnownCACertificates = &wk
		}, "unsupported wellKnownCACertificates"},
		{"ref kind", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs[0].Kind = "Bundle"
		}, "caCertificateRef kind is not supported"},
		{"ref group", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs[0].Group = "cert-manager.io"
		}, "caCertificateRef kind is not supported"},
		{"ref missing", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs[0].Name = "absent"
		}, "caCertificateRef not found"},
		{"ref without key", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs[0].Name = "no-key"
		}, "has no ca.crt key"},
		{"ref without a certificate", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs[0].Name = "garbage"
		}, "holds no parsable certificate"},
		{"secret ref", func(p *gwapiv1.BackendTLSPolicy) {
			p.Spec.Validation.CACertificateRefs[0].Kind = "Secret"
			p.Spec.Validation.CACertificateRefs[0].Name = "ca-secret"
		}, ""},
	}
	_ = str
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := load(t, filepath.Join("testdata", "backend-tls.yaml"))
			c.configMaps["shop/no-key"] = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "no-key"},
				Data:       map[string]string{"tls.crt": "x"}}
			c.configMaps["shop/garbage"] = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "garbage"},
				Data:       map[string]string{"ca.crt": "not a certificate"}}
			c.secrets["shop/ca-secret"] = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ca-secret"},
				Type:       corev1.SecretTypeTLS,
				Data:       map[string][]byte{"ca.crt": []byte(c.configMaps["shop/ca-bundle"].Data["ca.crt"])}}
			for _, p := range c.policies {
				if p.Name == "tls-ca" {
					tt.edit(p)
				}
			}
			model, _, problems := Translate(Config{
				Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
			})
			var secure ir.BackendMember
			for _, g := range model.Backends {
				for _, m := range g.Members {
					if m.Service.Name == "secure-svc" || strings.Contains(m.InvalidReason, "secure-svc") {
						secure = m
					}
				}
			}
			if tt.want == "" {
				require.False(t, secure.Invalid, "%v", details(problems))
				require.NotNil(t, secure.TLS)
				return
			}
			require.True(t, secure.Invalid)
			require.Contains(t, secure.InvalidReason, "BackendTLSPolicy/shop/tls-ca")
			require.Contains(t, secure.InvalidReason, tt.want)
			containing(t, problems, "backendRef", "tls-ca", tt.want)
		})
	}
}

func TestTranslateBackendTLSTargetKind(t *testing.T) {
	// A policy's targetRef must be a Service; anything else is reported against
	// the policy and governs nothing, so the next policy for the target takes it
	c := load(t, filepath.Join("testdata", "backend-tls.yaml"))
	for _, p := range c.policies {
		if p.Name == "tls-ca" {
			p.Spec.TargetRefs[0].Kind = "Deployment"
		}
	}
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "BackendTLSPolicy/shop/tls-ca", "targetRef kind /Deployment is not supported")
	var governed bool
	for _, g := range model.Backends {
		for _, m := range g.Members {
			if m.Service.Name == "secure-svc" {
				governed = true
				require.NotNil(t, m.TLS)
				require.Equal(t, "late.internal", m.TLS.Hostname,
					"the policy that lost to tls-ca now governs the target")
			}
		}
	}
	require.True(t, governed)

	// with no policy selecting the target at all, the connection is plaintext
	c.policies = nil
	model, _, _ = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	for _, g := range model.Backends {
		for _, m := range g.Members {
			require.Nil(t, m.TLS)
			require.Equal(t, ir.ProtocolHTTP, m.Service.Scheme)
		}
	}
}

func TestTranslateEndpointMode(t *testing.T) {
	require.Empty(t, golden(t, "endpoint"))
}

func TestTranslateParentRefProblems(t *testing.T) {
	// parentRefs that cannot attach are reported with the reason, except one
	// naming a Gateway this controller does not claim, which is not its business
	tests := []struct {
		name   string
		edit   func(*gwapiv1.ParentReference)
		detail string
	}{
		{"unsupported kind", func(pr *gwapiv1.ParentReference) {
			k := gwapiv1.Kind("Service")
			pr.Kind = &k
		}, "kind gateway.networking.k8s.io/Service is not supported"},
		{"no such section", func(pr *gwapiv1.ParentReference) {
			s := gwapiv1.SectionName("nope")
			pr.SectionName = &s
		}, `no listener matches sectionName "nope"`},
		{"no such port", func(pr *gwapiv1.ParentReference) {
			p := gwapiv1.PortNumber(9999)
			pr.Port = &p
		}, "no listener matches port 9999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
				test.edit(&hr.Spec.ParentRefs[0])
			})
			containing(t, problems, "HTTPRoute/shop/web", test.detail)
			require.Empty(t, model.Routes)
		})
	}

	// an unknown Gateway is silently not ours
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.ParentRefs[0].Name = "elsewhere"
	})
	require.Empty(t, problems)
	require.Empty(t, model.Routes)

	// a route hostname outside the listener's is not accepted there
	model, problems = mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Hostnames = []gwapiv1.Hostname{"other.net"}
	})
	require.Len(t, model.Routes, 1, "the basic listener names no hostname")
	require.Empty(t, problems)
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	h := gwapiv1.Hostname("shop.example.com")
	c.gateways[0].Spec.Listeners[0].Hostname = &h
	c.routes[0].Spec.Hostnames = []gwapiv1.Hostname{"other.net"}
	model, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "no listener hostname intersects")
	require.Empty(t, model.Routes)
}

func TestTranslateMatchRejections(t *testing.T) {
	// A piece of a rule the translator cannot use is dropped with a reason
	exact := gwapiv1.PathMatchExact
	regex := gwapiv1.PathMatchRegularExpression
	bogus := gwapiv1.PathMatchType("Fuzzy")
	rx := gwapiv1.HeaderMatchRegularExpression
	qrx := gwapiv1.QueryParamMatchRegularExpression
	tests := []struct {
		name   string
		match  gwapiv1.HTTPRouteMatch
		detail string
	}{
		{"relative path", gwapiv1.HTTPRouteMatch{Path: &gwapiv1.HTTPPathMatch{
			Type: &exact, Value: new("api")}}, "must begin with '/'"},
		{"bad regex", gwapiv1.HTTPRouteMatch{Path: &gwapiv1.HTTPPathMatch{
			Type: &regex, Value: new("/api/(")}}, "not a valid regular expression"},
		{"unknown type", gwapiv1.HTTPRouteMatch{Path: &gwapiv1.HTTPPathMatch{
			Type: &bogus, Value: new("/api")}}, "unsupported path match type"},
		{"bad header regex", gwapiv1.HTTPRouteMatch{Headers: []gwapiv1.HTTPHeaderMatch{{
			Type: &rx, Name: "X-A", Value: "("}}}, `header "X-A": not a valid regular expression`},
		{"bad query regex", gwapiv1.HTTPRouteMatch{QueryParams: []gwapiv1.HTTPQueryParamMatch{{
			Type: &qrx, Name: "q", Value: "("}}}, `query parameter "q": not a valid regular expression`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
				hr.Spec.Rules[0].Matches = []gwapiv1.HTTPRouteMatch{test.match}
			})
			containing(t, problems, "HTTPRoute/shop/web", "rule 0 match 0", test.detail)
			require.Empty(t, model.Routes, "a rule whose only match is dropped serves nothing")
		})
	}
}

func TestTranslateBackendRefRejections(t *testing.T) {
	// A backendRef the translator cannot resolve keeps its slot and is reported
	svcKind := gwapiv1.Kind("Bucket")
	otherGroup := gwapiv1.Group("storage.example.com")
	tests := []struct {
		name   string
		edit   func(*gwapiv1.HTTPBackendRef)
		detail string
	}{
		{"unsupported kind", func(r *gwapiv1.HTTPBackendRef) {
			r.Kind, r.Group = &svcKind, &otherGroup
		}, "kind storage.example.com/Bucket is not supported"},
		{"no port", func(r *gwapiv1.HTTPBackendRef) { r.Port = nil }, "no port named"},
		{"wrong port", func(r *gwapiv1.HTTPBackendRef) {
			p := gwapiv1.PortNumber(9999)
			r.Port = &p
		}, "has no tcp port 9999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
				test.edit(&hr.Spec.Rules[0].BackendRefs[0])
			})
			containing(t, problems, "HTTPRoute/shop/web", "backendRef 0", test.detail)
			require.Len(t, model.Backends, 1)
			require.True(t, model.Backends[0].Members[0].Invalid)
		})
	}
}

func TestTranslateBadRouteHostname(t *testing.T) {
	// A route hostname the router cannot register fails the whole route, because
	// serving it on the others would widen a boundary the author drew
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Hostnames = append(hr.Spec.Hostnames, "a.*.example.com")
	})
	containing(t, problems, `hostname "a.*.example.com" is not routable`)
	require.Empty(t, model.Routes)
}

func TestTranslateGatewayRejections(t *testing.T) {
	// Gateway features outside this build's scope are reported and ignored, and
	// a listener's own problems do not take its siblings down
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	gw := c.gateways[0]
	gw.Spec.AllowedListeners = &gwapiv1.AllowedListeners{}
	gw.Spec.TLS = &gwapiv1.GatewayTLSConfig{}
	gw.Spec.Addresses = []gwapiv1.GatewaySpecAddress{{Value: "1.2.3.4"}}
	bad := gwapiv1.Hostname("*.*.example.com")
	mode := gwapiv1.TLSModeTerminate
	gw.Spec.Listeners = append(gw.Spec.Listeners,
		gwapiv1.Listener{Name: "bad-host", Port: 81, Protocol: gwapiv1.HTTPProtocolType,
			Hostname: &bad},
		gwapiv1.Listener{Name: "tls-on-http", Port: 82, Protocol: gwapiv1.HTTPProtocolType,
			TLS: &gwapiv1.ListenerTLSConfig{}},
		gwapiv1.Listener{Name: "no-tls", Port: 443, Protocol: gwapiv1.HTTPSProtocolType},
		gwapiv1.Listener{Name: "no-refs", Port: 444, Protocol: gwapiv1.HTTPSProtocolType,
			TLS: &gwapiv1.ListenerTLSConfig{Mode: &mode}},
		gwapiv1.Listener{Name: "missing-secret", Port: 445, Protocol: gwapiv1.HTTPSProtocolType,
			TLS: &gwapiv1.ListenerTLSConfig{CertificateRefs: []gwapiv1.SecretObjectReference{
				{Name: "absent"}}}},
	)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "allowedListeners is not supported")
	containing(t, problems, "spec.tls is not supported")
	containing(t, problems, "spec.addresses is not supported")
	containing(t, problems, `listener "bad-host"`, "is not routable")
	containing(t, problems, `listener "tls-on-http"`, "tls is ignored")
	containing(t, problems, `listener "no-tls"`, "requires a tls block")
	containing(t, problems, `listener "no-refs"`, "names no certificateRefs")
	containing(t, problems, `listener "missing-secret"`, `tls secret "infra/absent": secret not found`)
	names := make([]string, 0, len(model.Listeners))
	for _, l := range model.Listeners {
		names = append(names, l.Name)
	}
	// the listener whose Secret is merely absent still opens, so a Secret
	// created later heals it; the ones that cannot terminate TLS do not
	require.ElementsMatch(t, []string{"Gateway/infra/gw/http", "Gateway/infra/gw/tls-on-http",
		"Gateway/infra/gw/missing-secret"}, names)
}

func TestTranslateAllowedRoutesSelectorProblems(t *testing.T) {
	// A Selector listener with no selector, or an invalid one, admits nothing
	// and says so rather than admitting everything
	sel := gwapiv1.NamespacesFromSelector
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.Listeners[0].AllowedRoutes = &gwapiv1.AllowedRoutes{
		Namespaces: &gwapiv1.RouteNamespaces{From: &sel}}
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "but no selector is set")
	require.Empty(t, model.Routes)

	c.gateways[0].Spec.Listeners[0].AllowedRoutes.Namespaces.Selector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "team", Operator: "Bogus"}}}
	model, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "selector is invalid")
	require.Empty(t, model.Routes)

	weird := gwapiv1.FromNamespaces("Sometimes")
	c.gateways[0].Spec.Listeners[0].AllowedRoutes = &gwapiv1.AllowedRoutes{
		Namespaces: &gwapiv1.RouteNamespaces{From: &weird}}
	_, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, `"Sometimes" is not recognized`)
}

func TestTranslateSelfConflict(t *testing.T) {
	// The same match declared twice in one route is its own conflict
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules = append(hr.Spec.Rules, hr.Spec.Rules[0])
	})
	containing(t, problems, "declared more than once")
	require.Len(t, model.Backends, 1)
}

func TestIntersectHostnames(t *testing.T) {
	tests := []struct {
		listener string
		route    []string
		want     []string
		ok       bool
	}{
		{"", nil, nil, true},
		{"", []string{"b.com", "a.com", "a.com"}, []string{"a.com", "b.com"}, true},
		{"a.example.com", nil, []string{"a.example.com"}, true},
		{"a.example.com", []string{"a.example.com"}, []string{"a.example.com"}, true},
		{"a.example.com", []string{"*.example.com"}, []string{"a.example.com"}, true},
		{"a.example.com", []string{"b.example.com"}, nil, false},
		{"*.example.com", []string{"a.example.com", "x.y.example.com", "example.com"},
			[]string{"a.example.com", "x.y.example.com"}, true},
		{"*.example.com", []string{"*.example.com"}, []string{"*.example.com"}, true},
		{"*.example.com", []string{"*.a.example.com"}, []string{"*.a.example.com"}, true},
		{"*.a.example.com", []string{"*.example.com"}, []string{"*.a.example.com"}, true},
		{"*.example.com", []string{"*.example.net"}, nil, false},
		{"*.example.com", []string{"example.com"}, nil, false},
	}
	for _, test := range tests {
		got, ok := intersectHostnames(test.listener, test.route)
		require.Equal(t, test.ok, ok, "%q ∩ %v", test.listener, test.route)
		require.Equal(t, test.want, got, "%q ∩ %v", test.listener, test.route)
	}
}

func TestNormalizeHostname(t *testing.T) {
	for in, want := range map[string]string{
		" Shop.Example.COM ": "shop.example.com",
		"*.example.com":      "*.example.com",
	} {
		got, err := translate.Hostname(in, translate.HostnameRequired)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, bad := range []string{"", "*", "*.", "a.*.example.com", "*.*.example.com", "a*.com"} {
		_, err := translate.Hostname(bad, translate.HostnameRequired)
		require.Error(t, err, bad)
	}
}

func serveGenerated(t *testing.T, name string,
	mutate ...func(*kubecfg.Options),
) router.Router {
	// serveGenerated compiles a fixture and serves it through the real loader and
	// router, with one origin per Service whose response names it
	t.Helper()
	model, _, o := translateFixture(t, name, mutate...)
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	return serveOverlay(t, overlay)
}

func serveGeneratedWith(t *testing.T, name string, origin http.HandlerFunc,
	mutate ...func(*kubecfg.Options),
) router.Router {
	// serveGeneratedWith is serveGenerated with one origin handler standing in
	// for every Service, for asserting what reaches the origin
	t.Helper()
	model, _, o := translateFixture(t, name, mutate...)
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	return serveOverlayWith(t, overlay, origin)
}

func serveOverlay(t *testing.T, overlay *config.Overlay) router.Router {
	// serveOverlay loads a compiled overlay through the real loader and route
	// registration and returns a router over it
	t.Helper()
	return serveOverlayWith(t, overlay, nil)
}

func requestFull(rtr router.Router, method, host, path string, headers ...string,
) (int, string, http.Header) {
	// requestFull issues one request and returns the status, body and headers
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code, w.Body.String(), w.Header()
}

func serveOverlayWith(t *testing.T, overlay *config.Overlay, origin http.HandlerFunc) router.Router {
	// serveOverlayWith is serveOverlay with an optional origin handler in place
	// of the default one that names the Service
	t.Helper()
	return serveOverlayOn(t, overlay, baseConfig, origin)[""]
}

func serveOverlayOn(t *testing.T, overlay *config.Overlay, base string,
	origin http.HandlerFunc,
) map[string]router.Router {
	// serveOverlayOn loads a compiled overlay onto the file configuration through the real
	// loader, returning one router per listener plus one under the empty name holding every backend
	t.Helper()
	_, _, routers := loadOverlay(t, overlay, base, origin, false)
	return routers
}

func loadOverlay(t *testing.T, overlay *config.Overlay, base string,
	origin http.HandlerFunc, tlsOrigins bool,
) (*config.Config, backends.Backends, map[string]router.Router) {
	// loadOverlay loads a compiled overlay onto the file configuration through the real loader
	// and route registration, with one origin per Service; the origins terminate TLS when asked
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(base), 0o600))
	conf, err := config.LoadWithOverlay([]string{"-config", path}, overlay)
	require.NoError(t, err)
	require.NoError(t, conf.Backends.Validate())
	require.NoError(t, validate.Validate(conf))

	origins := make(map[string]string)
	for _, b := range conf.Backends {
		if b.OriginURL == "" || !strings.Contains(b.OriginURL, ".svc:") {
			continue
		}
		b.OriginURL = originFor(t, origins, b.OriginURL, origin, tlsOrigins)
		parsed, err := neturl.Parse(b.OriginURL)
		require.NoError(t, err)
		b.Scheme, b.Host, b.PathPrefix = parsed.Scheme, parsed.Host, parsed.Path
	}
	require.NoError(t, conf.Process())
	// the daemon builds the authenticator instances at setup, after the
	// configuration is validated and before routes are registered
	for _, ao := range conf.Authenticators {
		ac, err := authregistry.New(ao.Provider, map[string]any{"options": ao})
		require.NoError(t, err)
		ao.Authenticator = ac
	}
	clients := make(backends.Backends, len(conf.Backends))
	require.NoError(t, validate.RoutesRulesAndPools(conf, clients))
	caches := cacheregistry.LoadCachesFromConfig(conf)
	t.Cleanup(func() { cacheregistry.CloseCaches(caches) })
	routers := map[string]router.Router{"": lm.NewRouter()}
	require.NoError(t, routing.RegisterProxyRoutes(conf, clients, routers[""], nil,
		caches, nil, false))
	perListener := make(map[string]router.Router, len(conf.Listeners))
	for name := range conf.Listeners {
		perListener[name] = lm.NewRouter()
	}
	require.NoError(t, routing.RegisterProxyRoutesForListeners(conf, clients,
		perListener, nil, caches, nil, false))
	maps.Copy(routers, perListener)
	require.NoError(t, alb.StartALBPools(clients, healthcheck.StatusLookup{}))
	t.Cleanup(func() { _ = alb.StopPools(clients) })
	return conf, clients, routers
}

func originFor(t *testing.T, origins map[string]string, clusterURL string,
	handler http.HandlerFunc, tlsOrigin bool,
) string {
	// originFor returns a server standing in for one in-cluster Service, whose
	// response names the Service unless a handler is supplied
	t.Helper()
	if url, ok := origins[clusterURL]; ok {
		return url
	}
	u, err := neturl.Parse(clusterURL)
	require.NoError(t, err)
	service, _, _ := strings.Cut(u.Host, ".")
	if handler == nil {
		handler = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(service))
		}
	}
	// an origin also speaks cleartext HTTP/2 by prior knowledge, as a gRPC Service does
	srv := httptest.NewUnstartedServer(h2c.NewHandler(handler, &http2.Server{}))
	if tlsOrigin {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	origins[clusterURL] = srv.URL
	return srv.URL
}

func request(rtr router.Router, method, host, path string, headers ...string) (int, string) {
	// request issues one request and returns the status and the body
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestApplyParameterRejections(t *testing.T) {
	// Every parameter key validates its value and a bad one is refused alone
	tr := &translator{known: known()}
	tests := map[string][2]string{
		"empty":                 {ParamCacheName, ""},
		"unknown key":           {"colour", "blue"},
		"bad routing mode":      {ParamRoutingMode, "sideways"},
		"bad health mode":       {ParamHealthMode, "guess"},
		"bad timeout":           {ParamTimeout, "45"},
		"negative timeout":      {ParamTimeout, "-1s"},
		"unknown cache":         {ParamCacheName, "nope"},
		"unknown negative":      {ParamNegativeCacheName, "nope"},
		"unknown tracer":        {ParamTracingName, "nope"},
		"unknown rewriter":      {ParamReqRewriterName, "nope"},
		"unknown authenticator": {ParamAuthenticatorName, "nope"},
	}
	for name, kv := range tests {
		t.Run(name, func(t *testing.T) {
			var p ir.Policy
			require.Error(t, tr.applyParameter(&p, kv[0], kv[1]))
			require.Equal(t, ir.Policy{}, p, "a rejected value must not be applied")
		})
	}
	var p ir.Policy
	require.NoError(t, tr.applyParameter(&p, ParamNegativeCacheName, "api-errors"))
	require.Equal(t, "api-errors", p.NegativeCacheName)
	// with nothing to check against, any name is accepted
	free := &translator{}
	require.NoError(t, free.applyParameter(&p, ParamReqRewriterName, "anything"))
	require.Equal(t, "anything", p.ReqRewriterName)
}

func TestGrantIndex(t *testing.T) {
	// A grant permits only what both its from and to entries describe
	name := gwapiv1.ObjectName("shared")
	g := indexGrants([]*gwapiv1.ReferenceGrant{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "backends", Name: "g"},
		Spec: gwapiv1.ReferenceGrantSpec{
			From: []gwapiv1.ReferenceGrantFrom{{
				Group: gwapiv1.GroupName, Kind: kindHTTPRoute, Namespace: "shop"}},
			To: []gwapiv1.ReferenceGrantTo{
				{Group: "", Kind: kindService, Name: &name},
				{Group: "", Kind: kindSecret},
			},
		},
	}})
	from := reference{group: gwapiv1.GroupName, kind: kindHTTPRoute, namespace: "shop"}
	require.True(t, g.permits(from, reference{kind: kindService, namespace: "backends", name: "shared"}))
	require.False(t, g.permits(from, reference{kind: kindService, namespace: "backends", name: "other"}),
		"a named to entry permits only that object")
	require.True(t, g.permits(from, reference{kind: kindSecret, namespace: "backends", name: "any"}),
		"an unnamed to entry permits every object of its kind")
	require.False(t, g.permits(from, reference{kind: kindConfigMap, namespace: "backends"}))
	require.False(t, g.permits(reference{group: gwapiv1.GroupName, kind: kindGateway, namespace: "shop"},
		reference{kind: kindSecret, namespace: "backends"}), "the from kind must match")
	require.False(t, g.permits(reference{group: gwapiv1.GroupName, kind: kindHTTPRoute, namespace: "other"},
		reference{kind: kindSecret, namespace: "backends"}), "the from namespace must match")
	require.False(t, g.permits(from, reference{kind: kindSecret, namespace: "elsewhere"}),
		"only the target namespace's grants count")
}

func TestTranslateCertificateRefKind(t *testing.T) {
	// A certificateRef of an unsupported kind is refused on its own
	c := load(t, filepath.Join("testdata", "listeners.yaml"))
	kind := gwapiv1.Kind("ClusterTrustBundle")
	c.gateways[0].Spec.Listeners[1].TLS.CertificateRefs = append(
		c.gateways[0].Spec.Listeners[1].TLS.CertificateRefs,
		gwapiv1.SecretObjectReference{Kind: &kind, Name: "bundle"})
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, `listener "https"`, "certificateRef kind /ClusterTrustBundle is not supported")
	require.Len(t, model.Certs, 1, "the supported reference still serves")
}

func TestByAgeIsTotal(t *testing.T) {
	// Objects created in the same clock tick are ordered by namespace then name,
	// so every replica awards conflicts identically
	mk := func(ns, name string, sec int64) *gwapiv1.HTTPRoute {
		return &gwapiv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{
			Namespace: ns, Name: name, CreationTimestamp: metav1.Unix(sec, 0)}}
	}
	in := []*gwapiv1.HTTPRoute{mk("b", "x", 1), mk("a", "z", 1), mk("a", "y", 1), mk("z", "a", 0)}
	out := translate.ByAge(in)
	got := make([]string, 0, len(out))
	for _, r := range out {
		got = append(got, r.Namespace+"/"+r.Name)
	}
	require.Equal(t, []string{"z/a", "a/y", "a/z", "b/x"}, got)
	require.Equal(t, "b/x", in[0].Namespace+"/"+in[0].Name, "the input is not reordered")
}

func TestTranslateDeclaredExactBeatsAnOlderPrefix(t *testing.T) {
	// A match declared Exact outranks the exact half an older route's prefix
	// lowered into; the prefix keeps everything below the path
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.routes[0].CreationTimestamp = metav1.Unix(1, 0)
	c.services["shop/exact-svc"] = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "exact-svc"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	exact := gwapiv1.PathMatchExact
	port := gwapiv1.PortNumber(8080)
	c.routes = append(c.routes, &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "newer",
			CreationTimestamp: metav1.Unix(2, 0)},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{
				Name: "gw", Namespace: new(gwapiv1.Namespace("infra"))}}},
			Hostnames: []gwapiv1.Hostname{"shop.example.com"},
			Rules: []gwapiv1.HTTPRouteRule{{
				Matches: []gwapiv1.HTTPRouteMatch{{Path: &gwapiv1.HTTPPathMatch{
					Type: &exact, Value: new("/api")}}},
				BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: gwapiv1.BackendRef{
					BackendObjectReference: gwapiv1.BackendObjectReference{
						Name: "exact-svc", Port: &port}}}},
			}},
		},
	})
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.Empty(t, problems, "losing only the exact half is precedence, not a conflict")
	rtr := serveModel(t, model, options(t))
	_, body := request(rtr, http.MethodGet, "shop.example.com", "/api")
	require.Equal(t, "exact-svc", body)
	_, body = request(rtr, http.MethodGet, "shop.example.com", "/api/orders")
	require.Equal(t, "web-svc", body)
}

func TestTranslateMarksDeclaredMethodsOnly(t *testing.T) {
	// A method a match was awarded after losing others is not a declaration,
	// and only a declared method is marked as one
	model, _, _ := translateFixture(t, "precedence")
	var declared, awarded int
	for _, r := range model.Routes {
		for _, rule := range r.Rules {
			for _, m := range rule.Matches {
				switch {
				case m.MethodSpecific:
					declared++
					require.Equal(t, []string{http.MethodPost}, m.Methods)
				case len(m.Methods) > 0:
					awarded++
					require.NotContains(t, m.Methods, http.MethodPost)
				}
			}
		}
	}
	require.Equal(t, 1, declared, "the writer rule's match")
	require.Equal(t, 1, awarded, "the plain stable rule's match, less POST; the "+
		"header match's predicates differ, so it lost nothing")
}

func TestTranslateDeduplicatesConditionNames(t *testing.T) {
	// A later condition on a header already conditioned is ignored whatever its
	// case, as is a later condition on a query parameter of the same name
	model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules[0].Matches[0].Headers = []gwapiv1.HTTPHeaderMatch{
			{Name: "X-Tenant", Value: "a"}, {Name: "x-tenant", Value: "b"},
			{Name: "X-Other", Value: "c"},
		}
		hr.Spec.Rules[0].Matches[0].QueryParams = []gwapiv1.HTTPQueryParamMatch{
			{Name: "q", Value: "1"}, {Name: "q", Value: "2"}, {Name: "Q", Value: "3"},
		}
	})
	require.Empty(t, problems)
	m := model.Routes[0].Rules[0].Matches[0]
	require.Equal(t, []ir.KeyValueMatch{{Name: "X-Tenant", Value: "a"},
		{Name: "X-Other", Value: "c"}}, m.Headers, "the first declaration is kept")
	require.Equal(t, []ir.KeyValueMatch{{Name: "q", Value: "1"}, {Name: "Q", Value: "3"}},
		m.QueryParams, "query parameter names are case-sensitive")
}

func serveModel(t *testing.T, model *ir.IR, o *kubecfg.Options) router.Router {
	// serveModel loads a translated model the way the daemon does and returns a
	// router over it, with one origin per Service whose response names it
	t.Helper()
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	return serveOverlay(t, overlay)
}

func reportOf(t *testing.T, c *cache) (*ir.Report, []Problem) {
	// reportOf translates a cache and returns what would be written back
	t.Helper()
	_, report, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t), KnownNames: known,
	})
	return report, problems
}

func condOf(t *testing.T, conds []ir.Condition, typ string) ir.Condition {
	t.Helper()
	c, ok := ir.Find(conds, typ)
	require.True(t, ok, "no %s condition in %v", typ, conds)
	return c
}

func reasonOf(problems []Problem, reason string) bool {
	return slices.ContainsFunc(problems, func(p Problem) bool { return p.EventReason() == reason })
}

func TestReportBasic(t *testing.T) {
	// Everything in the basic fixture is accepted, and the report says so at every level
	report, _ := reportOf(t, load(t, filepath.Join("testdata", "basic.yaml")))
	require.Len(t, report.Classes, 1)
	require.Equal(t, "trickster", report.Classes[0].Source.Name)
	require.True(t, report.Classes[0].Accepted.Status)
	require.EqualValues(t, gwapiv1.GatewayClassReasonAccepted, report.Classes[0].Accepted.Reason)

	require.Len(t, report.Gateways, 1)
	gw := report.Gateways[0]
	require.Equal(t, "Gateway/infra/gw", gw.Source.Key())
	require.True(t, condOf(t, gw.Conditions, "Accepted").Status)
	require.EqualValues(t, gwapiv1.GatewayReasonAccepted, condOf(t, gw.Conditions, "Accepted").Reason)
	require.True(t, condOf(t, gw.Conditions, "Programmed").Status)
	require.Len(t, gw.Listeners, 1)
	l := gw.Listeners[0]
	require.Equal(t, "http", l.Name)
	require.Equal(t, []string{"HTTPRoute", "GRPCRoute"}, l.SupportedKinds)
	require.Equal(t, 1, l.AttachedRoutes)
	require.True(t, condOf(t, l.Conditions, "Accepted").Status)
	require.True(t, condOf(t, l.Conditions, "Programmed").Status)
	require.True(t, condOf(t, l.Conditions, "ResolvedRefs").Status)
	require.False(t, condOf(t, l.Conditions, "Conflicted").Status)

	require.Len(t, report.Routes, 1)
	r := report.Routes[0]
	require.Equal(t, "HTTPRoute/shop/web", r.Source.Key())
	require.Len(t, r.Parents, 1)
	require.Equal(t, ir.ParentRef{Namespace: "infra", Name: "gw"}, r.Parents[0].Ref)
	require.True(t, condOf(t, r.Parents[0].Conditions, "Accepted").Status)
	require.EqualValues(t, gwapiv1.RouteReasonAccepted,
		condOf(t, r.Parents[0].Conditions, "Accepted").Reason)
	require.True(t, condOf(t, r.Parents[0].Conditions, "ResolvedRefs").Status)
}

func TestReportListenerRejections(t *testing.T) {
	// A refused listener keeps its status entry, saying why, and the Gateway's
	// conditions follow from how many listeners survived
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.Listeners[0].Protocol = gwapiv1.ProtocolType("example.com/Custom")
	report, _ := reportOf(t, c)
	gw := report.Gateways[0]
	accepted := condOf(t, gw.Conditions, "Accepted")
	require.False(t, accepted.Status)
	require.EqualValues(t, gwapiv1.GatewayReasonListenersNotValid, accepted.Reason)
	require.False(t, condOf(t, gw.Conditions, "Programmed").Status)
	require.Len(t, gw.Listeners, 1)
	l := gw.Listeners[0]
	require.EqualValues(t, gwapiv1.ListenerReasonUnsupportedProtocol,
		condOf(t, l.Conditions, "Accepted").Reason)
	require.False(t, condOf(t, l.Conditions, "Programmed").Status)
	require.EqualValues(t, gwapiv1.ListenerReasonInvalid, condOf(t, l.Conditions, "Programmed").Reason)
	// with no listener the route has no parent to attach to
	require.Len(t, report.Routes, 1)
	require.EqualValues(t, gwapiv1.RouteReasonNoMatchingParent,
		condOf(t, report.Routes[0].Parents[0].Conditions, "Accepted").Reason)

	// conflicts: a second protocol on the port, and a repeated hostname
	c = load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.Listeners = append(c.gateways[0].Spec.Listeners,
		gwapiv1.Listener{Name: "tls", Port: 80, Protocol: gwapiv1.HTTPSProtocolType,
			TLS: &gwapiv1.ListenerTLSConfig{CertificateRefs: []gwapiv1.SecretObjectReference{{Name: "x"}}}},
		gwapiv1.Listener{Name: "dup", Port: 80, Protocol: gwapiv1.HTTPProtocolType})
	report, _ = reportOf(t, c)
	gw = report.Gateways[0]
	accepted = condOf(t, gw.Conditions, "Accepted")
	require.True(t, accepted.Status)
	require.EqualValues(t, gwapiv1.GatewayReasonListenersNotValid, accepted.Reason)
	require.Contains(t, accepted.Message, "2 of 3")
	require.Len(t, gw.Listeners, 3)
	tls := gw.Listeners[1]
	require.True(t, condOf(t, tls.Conditions, "Conflicted").Status)
	require.EqualValues(t, gwapiv1.ListenerReasonProtocolConflict,
		condOf(t, tls.Conditions, "Conflicted").Reason)
	require.False(t, condOf(t, tls.Conditions, "Accepted").Status)
	dup := gw.Listeners[2]
	require.EqualValues(t, gwapiv1.ListenerReasonHostnameConflict,
		condOf(t, dup.Conditions, "Conflicted").Reason)
	require.Equal(t, 1, gw.Listeners[0].AttachedRoutes)
}

func TestReportCertificateRefs(t *testing.T) {
	// A certificate that does not resolve lowers ResolvedRefs without refusing
	// the listener, and is reported under the certificate reason
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.Listeners[0].Protocol = gwapiv1.HTTPSProtocolType
	c.gateways[0].Spec.Listeners[0].TLS = &gwapiv1.ListenerTLSConfig{
		CertificateRefs: []gwapiv1.SecretObjectReference{{Name: "missing"}}}
	report, problems := reportOf(t, c)
	l := report.Gateways[0].Listeners[0]
	require.True(t, condOf(t, l.Conditions, "Accepted").Status)
	resolved := condOf(t, l.Conditions, "ResolvedRefs")
	require.False(t, resolved.Status)
	require.EqualValues(t, gwapiv1.ListenerReasonInvalidCertificateRef, resolved.Reason)
	require.True(t, reasonOf(problems, ir.ReasonInvalidCertificate))

	// no certificateRefs at all refuses the listener on the same condition
	c.gateways[0].Spec.Listeners[0].TLS.CertificateRefs = nil
	report, _ = reportOf(t, c)
	l = report.Gateways[0].Listeners[0]
	require.False(t, condOf(t, l.Conditions, "ResolvedRefs").Status)
	require.False(t, condOf(t, l.Conditions, "Programmed").Status)

	// a route kind this controller does not serve lowers ResolvedRefs and the supported kinds
	c = load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.Listeners[0].AllowedRoutes.Kinds = []gwapiv1.RouteGroupKind{{Kind: "TCPRoute"}}
	report, _ = reportOf(t, c)
	l = report.Gateways[0].Listeners[0]
	require.EqualValues(t, gwapiv1.ListenerReasonInvalidRouteKinds,
		condOf(t, l.Conditions, "ResolvedRefs").Reason)
	require.Empty(t, l.SupportedKinds)
}

func TestReportRouteParents(t *testing.T) {
	// Each way a route can fail to attach names its own reason, and a route
	// that is not served does not count as attached
	section := gwapiv1.SectionName("nope")
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.routes[0].Spec.ParentRefs[0].SectionName = &section
	report, _ := reportOf(t, c)
	p := report.Routes[0].Parents[0]
	require.Equal(t, "nope", p.Ref.SectionName)
	require.EqualValues(t, gwapiv1.RouteReasonNoMatchingParent,
		condOf(t, p.Conditions, "Accepted").Reason)
	require.Equal(t, 0, report.Gateways[0].Listeners[0].AttachedRoutes)

	c = load(t, filepath.Join("testdata", "basic.yaml"))
	same := gwapiv1.NamespacesFromSame
	c.gateways[0].Spec.Listeners[0].AllowedRoutes.Namespaces.From = &same
	report, _ = reportOf(t, c)
	require.EqualValues(t, gwapiv1.RouteReasonNotAllowedByListeners,
		condOf(t, report.Routes[0].Parents[0].Conditions, "Accepted").Reason)

	c = load(t, filepath.Join("testdata", "basic.yaml"))
	other := gwapiv1.Hostname("*.other.example")
	c.gateways[0].Spec.Listeners[0].Hostname = &other
	report, _ = reportOf(t, c)
	require.EqualValues(t, gwapiv1.RouteReasonNoMatchingListenerHostname,
		condOf(t, report.Routes[0].Parents[0].Conditions, "Accepted").Reason)

	c = load(t, filepath.Join("testdata", "basic.yaml"))
	c.routes[0].Spec.Hostnames = []gwapiv1.Hostname{""}
	report, _ = reportOf(t, c)
	require.EqualValues(t, gwapiv1.RouteReasonUnsupportedValue,
		condOf(t, report.Routes[0].Parents[0].Conditions, "Accepted").Reason)

	c = load(t, filepath.Join("testdata", "basic.yaml"))
	c.routes[0].Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
		Type:         gwapiv1.HTTPRouteFilterExtensionRef,
		ExtensionRef: &gwapiv1.LocalObjectReference{Group: "x", Kind: "Y", Name: "z"},
	}}
	report, _ = reportOf(t, c)
	accepted := condOf(t, report.Routes[0].Parents[0].Conditions, "Accepted")
	require.False(t, accepted.Status)
	require.EqualValues(t, gwapiv1.RouteReasonUnsupportedValue, accepted.Reason)
	require.Equal(t, 0, report.Gateways[0].Listeners[0].AttachedRoutes)

	// a parentRef naming a Gateway this controller does not claim gets no entry
	c = load(t, filepath.Join("testdata", "basic.yaml"))
	c.routes[0].Spec.ParentRefs[0].Name = "theirs"
	report, _ = reportOf(t, c)
	require.Empty(t, report.Routes)
}

func TestReportResolvedRefs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*gwapiv1.BackendObjectReference)
		reason gwapiv1.RouteConditionReason
	}{
		{"missing", func(r *gwapiv1.BackendObjectReference) { r.Name = "missing" },
			gwapiv1.RouteReasonBackendNotFound},
		{"kind", func(r *gwapiv1.BackendObjectReference) {
			k := gwapiv1.Kind("ConfigMap")
			r.Kind = &k
		}, gwapiv1.RouteReasonInvalidKind},
		{"namespace", func(r *gwapiv1.BackendObjectReference) {
			ns := gwapiv1.Namespace("other")
			r.Namespace = &ns
		}, gwapiv1.RouteReasonRefNotPermitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := load(t, filepath.Join("testdata", "basic.yaml"))
			tc.edit(&c.routes[0].Spec.Rules[0].BackendRefs[0].BackendObjectReference)
			report, _ := reportOf(t, c)
			p := report.Routes[0].Parents[0]
			require.True(t, condOf(t, p.Conditions, "Accepted").Status,
				"an unresolved backend does not detach the route")
			resolved := condOf(t, p.Conditions, "ResolvedRefs")
			require.False(t, resolved.Status)
			require.EqualValues(t, tc.reason, resolved.Reason)
		})
	}
}

func TestReportClassParameters(t *testing.T) {
	// A class whose parameters cannot be honored is not accepted, and neither are its Gateways
	c := load(t, filepath.Join("testdata", "class-params.yaml"))
	for _, cm := range c.configMaps {
		cm.Data["bogus"] = "x"
	}
	report, problems := reportOf(t, c)
	require.Len(t, report.Classes, 1)
	require.False(t, report.Classes[0].Accepted.Status)
	require.EqualValues(t, gwapiv1.GatewayClassReasonInvalidParameters,
		report.Classes[0].Accepted.Reason)
	require.Contains(t, report.Classes[0].Accepted.Message, "bogus")
	require.True(t, reasonOf(problems, ir.ReasonInvalidParameters))
	require.NotEmpty(t, report.Gateways)
	for _, gw := range report.Gateways {
		accepted := condOf(t, gw.Conditions, "Accepted")
		require.False(t, accepted.Status)
		require.EqualValues(t, gwapiv1.GatewayReasonInvalidParameters, accepted.Reason)
		// the listeners are described, valid in themselves, and not programmed
		require.NotEmpty(t, gw.Listeners)
		for _, l := range gw.Listeners {
			require.True(t, condOf(t, l.Conditions, "Accepted").Status)
			require.False(t, condOf(t, l.Conditions, "Programmed").Status)
			require.Equal(t, 0, l.AttachedRoutes)
		}
	}
	// the routes naming the refused Gateway are told, rather than left with their last status
	require.NotEmpty(t, report.Routes)
	for _, r := range report.Routes {
		require.NotEmpty(t, r.Parents)
		for _, p := range r.Parents {
			accepted := condOf(t, p.Conditions, "Accepted")
			require.False(t, accepted.Status)
			require.EqualValues(t, gwapiv1.RouteReasonNoMatchingParent, accepted.Reason)
			require.Contains(t, accepted.Message, "cannot be honored")
		}
	}
	require.True(t, reasonOf(problems, ir.ReasonRejected), "the routes are told by Event too")
}

func TestReportUnusableCertificate(t *testing.T) {
	// Certificate material that cannot be served lowers ResolvedRefs and is reported under the
	// certificate reason; the reference stays in the model so what is serving is not withdrawn
	c := load(t, filepath.Join("testdata", "listeners.yaml"))
	c.secrets["infra/a-tls"].Data["tls.crt"] = []byte("garbage")
	model, report, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t), KnownNames: known,
	})
	var lowered int
	for _, gw := range report.Gateways {
		for _, l := range gw.Listeners {
			resolved := condOf(t, l.Conditions, "ResolvedRefs")
			if resolved.Status {
				continue
			}
			lowered++
			require.EqualValues(t, gwapiv1.ListenerReasonInvalidCertificateRef, resolved.Reason)
			require.Contains(t, resolved.Message, "unusable")
			require.True(t, condOf(t, l.Conditions, "Programmed").Status,
				"the translator does not know what the listener already serves")
		}
	}
	require.Positive(t, lowered)
	require.True(t, reasonOf(problems, ir.ReasonInvalidCertificate))
	require.True(t, slices.ContainsFunc(model.Certs, func(c ir.CertRef) bool {
		return c.Name == "infra/a-tls"
	}), "the reference is kept for the certificate already serving")
}

func TestTranslateCachePolicies(t *testing.T) {
	// Policies layer least specific first: the Gateway's, the route's, the named rule's, then the
	// member's Service and port; a provider is withheld from a rule declaring one of its paths
	problems := golden(t, "cache-policy")
	require.Len(t, problems, 6, details(problems))
	containing(t, problems, "HTTPRoute/shop/query", "rule 0: provider \"prometheus\" is not applied",
		"/api/v1/query_range")
	containing(t, problems, "TricksterCachePolicy/shop/query-prom", "HTTPRoute/shop/query rule 0")
	containing(t, problems, "TricksterCachePolicy/shop/prom-service", "HTTPRoute/shop/query rule 0")
	containing(t, problems, "TricksterCachePolicy/shop/late", "already governed by the older policy")
	containing(t, problems, "TricksterCachePolicy/shop/broken", "spec.cacheName")
	containing(t, problems, "TricksterCachePolicy/shop/missing", "not found")

	model, _, _ := translateFixture(t, "cache-policy")
	rules := make(map[string]ir.Rule)
	members := make(map[string]ir.BackendMember)
	for _, r := range model.Routes {
		for i, rule := range r.Rules {
			rules[r.Source.Name+"/"+strconv.Itoa(i)] = rule
		}
	}
	for _, g := range model.Backends {
		members[g.Name] = g.Members[0]
	}
	const gw, route, rule = "TricksterCachePolicy/infra/gateway-policy",
		"TricksterCachePolicy/shop/metrics-route", "TricksterCachePolicy/shop/api-rule"
	require.Equal(t, gw+"+"+route+"+"+rule, rules["metrics/0"].Policy)
	require.Equal(t, gw+"+"+route, rules["metrics/1"].Policy)
	require.Equal(t, gw+"+TricksterCachePolicy/shop/query-prom+noprovider", rules["query/0"].Policy)
	require.Equal(t, "TricksterCachePolicy/shop/prom-service", members["HTTPRoute/shop/metrics|r0"].Policy)
	require.Equal(t, "TricksterCachePolicy/shop/web-port", members["HTTPRoute/shop/metrics|r1"].Policy)
	require.Equal(t, "TricksterCachePolicy/shop/prom-service+noprovider",
		members["HTTPRoute/shop/query|r0"].Policy)

	// the index's report describes every policy read, whatever became of it
	c := load(t, filepath.Join("testdata", "cache-policy.yaml"))
	report := policyIndex(c).Report()
	require.Len(t, report, 9)
	reasons := make(map[string]string, len(report))
	for _, ps := range report {
		reasons[ps.Source.Name] = ps.Ancestors[0].Conditions[0].Reason
	}
	require.Equal(t, map[string]string{
		"gateway-policy": "Accepted", "metrics-route": "Accepted", "api-rule": "Accepted",
		"prom-service": "Accepted", "web-port": "Accepted", "query-prom": "Accepted",
		"late": "Conflicted", "broken": "Invalid", "missing": "TargetNotFound",
	}, reasons)
}

func TestTranslateTimeoutsAndRetry(t *testing.T) {
	// A rule's timeouts and retry policy reach the path the engine serves the rule from: the
	// backend's own path when one backendRef serves it, each member's catch-all behind a
	// weighted dispatch; a zero duration bounds nothing
	require.Empty(t, golden(t, "timeouts"))
	model, _, _ := translateFixture(t, "timeouts")
	require.Len(t, model.Routes, 1)
	rules := model.Routes[0].Rules
	require.Len(t, rules, 3)
	require.Equal(t, &ir.RuleTimeouts{RequestMS: 5000, BackendRequestMS: 2000}, rules[0].Timeouts)
	require.Equal(t, &ir.RuleRetry{Codes: []int{502, 503}, Attempts: 2, BackoffMS: 100}, rules[0].Retry)
	require.Equal(t, &ir.RuleTimeouts{BackendRequestMS: 1500}, rules[1].Timeouts)
	require.Equal(t, &ir.RuleRetry{Attempts: 1}, rules[1].Retry)
	require.Nil(t, rules[2].Timeouts)
	require.Nil(t, rules[2].Retry)

	// values the API cannot express are refused with the route
	for _, tc := range []struct {
		name string
		edit func(*gwapiv1.HTTPRouteRule)
		want string
	}{
		{"backend over request", func(r *gwapiv1.HTTPRouteRule) {
			req, be := gwapiv1.Duration("1s"), gwapiv1.Duration("2s")
			r.Timeouts = &gwapiv1.HTTPRouteTimeouts{Request: &req, BackendRequest: &be}
		}, "timeouts.backendRequest must not exceed timeouts.request"},
		{"bad duration", func(r *gwapiv1.HTTPRouteRule) {
			d := gwapiv1.Duration("soon")
			r.Timeouts = &gwapiv1.HTTPRouteTimeouts{Request: &d}
		}, "timeouts.request: not a valid duration"},
		{"too many attempts", func(r *gwapiv1.HTTPRouteRule) {
			n := 99
			r.Retry = &gwapiv1.HTTPRouteRetry{Attempts: &n}
		}, "retry.attempts must be between 1 and"},
		{"bad code", func(r *gwapiv1.HTTPRouteRule) {
			r.Retry = &gwapiv1.HTTPRouteRetry{Codes: []gwapiv1.HTTPRouteRetryStatusCode{42}}
		}, "retry.codes must be valid HTTP status codes"},
		{"bad backoff", func(r *gwapiv1.HTTPRouteRule) {
			d := gwapiv1.Duration("-1s")
			r.Retry = &gwapiv1.HTTPRouteRetry{Backoff: &d}
		}, "retry.backoff: not a valid duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
				tc.edit(&hr.Spec.Rules[0])
			})
			containing(t, problems, tc.want, "the route is not served")
			require.Empty(t, model.Routes)
		})
	}
}

func TestRetryIsServed(t *testing.T) {
	// A retried rule reaches the origin again after a failure it is configured to retry, and
	// the client sees the successful attempt
	var hits atomic.Int32
	rtr := serveGeneratedWith(t, "timeouts", func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1)%2 == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	status, body := request(rtr, http.MethodGet, "shop.example.com", "/api/orders")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", body)
	require.EqualValues(t, 2, hits.Load(), "the 502 was retried once")
	// the rule with no codes retries connection failures only
	status, _ = request(rtr, http.MethodGet, "shop.example.com", "/split/x")
	require.Equal(t, http.StatusBadGateway, status)
	require.EqualValues(t, 3, hits.Load())
}

func TestTranslateMirror(t *testing.T) {
	// A rule's mirror and a member's are lowered with the Service they copy to and the share
	// copied; one naming an absent Service is dropped, lowering ResolvedRefs, and the rest of
	// the route is served
	problems := golden(t, "mirror")
	containing(t, problems, "HTTPRoute/shop/shadowed",
		"requestMirror: service shop/missing-svc not found; the mirror is not applied")
	require.Len(t, problems, 1, "%v", details(problems))
	model, report := translateReport(t, "mirror")
	require.Len(t, model.Routes, 1)
	rules := model.Routes[0].Rules
	require.Len(t, rules, 4)
	require.Len(t, rules[0].Filters, 1)
	require.Equal(t, ir.FilterMirror, rules[0].Filters[0].Type)
	m := rules[0].Filters[0].Mirror
	require.Equal(t, "shadow-svc", m.Service.Name)
	require.EqualValues(t, 9090, m.Service.Port)
	require.Equal(t, 100, m.Percent)
	require.Empty(t, rules[1].Filters)
	var canary ir.BackendMember
	for _, g := range model.Backends {
		if g.RuleIndex == 1 {
			canary = g.Members[1]
		}
	}
	require.Len(t, canary.Filters, 1)
	require.Equal(t, 25, canary.Filters[0].Mirror.Percent, "a fraction is a rounded percent")
	require.Empty(t, rules[2].Filters, "an unresolved mirror is dropped")
	require.False(t, condOf(t, report.Routes[0].Parents[0].Conditions, "ResolvedRefs").Status)

	// a mirror copying nothing is refused with the route
	zero := int32(0)
	m2, problems := mutateRoute(t, func(hr *gwapiv1.HTTPRoute) {
		hr.Spec.Rules[0].Filters = []gwapiv1.HTTPRouteFilter{{
			Type: gwapiv1.HTTPRouteFilterRequestMirror,
			RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{
				BackendRef: gwapiv1.BackendObjectReference{Name: "web-svc"}, Percent: &zero}}}
	})
	containing(t, problems, "requestMirror copies no request")
	require.Empty(t, m2.Routes)
}

func TestMirrorIsServed(t *testing.T) {
	// A mirrored request reaches the shadow Service as well as the one that answers it, and
	// only the latter's response reaches the client
	var mu sync.Mutex
	hosts := make(map[string]int)
	rtr := serveGeneratedWith(t, "mirror", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts[originAddr(r)]++
		mu.Unlock()
		_, _ = w.Write([]byte("from " + originAddr(r)))
	})
	status, body := request(rtr, http.MethodGet, "shop.example.com", "/api/orders")
	require.Equal(t, http.StatusOK, status)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(hosts) == 2
	}, 5*time.Second, 10*time.Millisecond, "the request reaches two origins")
	mu.Lock()
	for host, n := range hosts {
		require.Equal(t, 1, n, host)
	}
	mu.Unlock()
	require.Contains(t, body, "from ")
	hosts = map[string]int{}
	status, _ = request(rtr, http.MethodGet, "shop.example.com", "/unmirrored")
	require.Equal(t, http.StatusOK, status)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	require.Len(t, hosts, 1, "a dropped mirror copies nothing")
	mu.Unlock()
}

func TestTranslateGRPCRoute(t *testing.T) {
	// A GRPCRoute is lowered onto the paths gRPC uses on the wire, ranks by age with the
	// HTTPRoutes it competes with, and refuses a newer HTTPRoute sharing a listener and
	// hostname, as the API defines for the two kinds
	problems := golden(t, "grpc")
	containing(t, problems, "HTTPRoute/shop/clash", "the hostnames of GRPCRoute/shop/rpc intersect")
	require.Len(t, problems, 1, "%v", details(problems))
	model, report := translateReport(t, "grpc")
	var rpc *ir.Route
	for i := range model.Routes {
		if model.Routes[i].Source.Kind == ir.KindGRPCRoute {
			rpc = &model.Routes[i]
		}
	}
	require.NotNil(t, rpc)
	require.Equal(t, ir.ProtocolGRPC, rpc.Protocol)
	require.Len(t, rpc.Rules, 2)
	paths := make(map[string]ir.PathMatch)
	for _, rule := range rpc.Rules {
		for _, m := range rule.Matches {
			require.Equal(t, []string{http.MethodPost}, m.Methods)
			require.True(t, m.MethodSpecific)
			paths[m.Path.Value] = m.Path
		}
	}
	require.Equal(t, ir.PathPrefix, paths["/shop.v1.Orders"].Type)
	require.Equal(t, ir.PathExact, paths["/shop.v1.Carts/GetCart"].Type)
	require.Equal(t, ir.PathRegex, paths["^/[^/]+/Health$"].Type)
	require.Equal(t, ir.PathRegex, paths[`^/(?:shop\.v[0-9]+\.Users)/(?:Get.*)$`].Type)
	require.Equal(t, "x-tenant", rpc.Rules[0].Matches[1].Headers[0].Name)

	for _, r := range report.Routes {
		accepted := condOf(t, r.Parents[0].Conditions, "Accepted")
		switch r.Source.Key() {
		case "HTTPRoute/shop/clash":
			require.False(t, accepted.Status)
			require.Equal(t, routeConflictReason, accepted.Reason)
		default:
			require.True(t, accepted.Status, r.Source.Key())
		}
	}
	for _, l := range report.Gateways[0].Listeners {
		switch l.Name {
		case "http":
			require.Equal(t, 2, l.AttachedRoutes, "the refused route is not attached")
		case "grpc":
			require.Equal(t, []string{"GRPCRoute"}, l.SupportedKinds)
			require.Equal(t, 1, l.AttachedRoutes)
		}
	}

	// method matches lower onto every path shape
	for _, tc := range []struct {
		m    gwapiv1.GRPCMethodMatch
		typ  gwapiv1.PathMatchType
		want string
	}{
		{gwapiv1.GRPCMethodMatch{}, gwapiv1.PathMatchRegularExpression, grpcCatchAll},
		{gwapiv1.GRPCMethodMatch{Service: new("a.B")}, gwapiv1.PathMatchPathPrefix, "/a.B"},
		{gwapiv1.GRPCMethodMatch{Service: new("a.B"), Method: new("C")}, gwapiv1.PathMatchExact, "/a.B/C"},
		{gwapiv1.GRPCMethodMatch{Method: new("C.D")}, gwapiv1.PathMatchRegularExpression,
			`^/[^/]+/C\.D$`},
		{gwapiv1.GRPCMethodMatch{Type: ptrOf(gwapiv1.GRPCMethodMatchRegularExpression),
			Service: new("a.*")}, gwapiv1.PathMatchRegularExpression, "^/(?:a.*)/[^/]+$"},
		{gwapiv1.GRPCMethodMatch{Type: ptrOf(gwapiv1.GRPCMethodMatchRegularExpression),
			Method: new("Get.*")}, gwapiv1.PathMatchRegularExpression, "^/[^/]+/(?:Get.*)$"},
	} {
		pm, err := grpcPath(&tc.m)
		require.NoError(t, err)
		require.Equal(t, tc.typ, *pm.Type, tc.want)
		require.Equal(t, tc.want, *pm.Value)
	}
	pm, err := grpcPath(nil)
	require.NoError(t, err)
	require.Equal(t, grpcCatchAll, *pm.Value)
	_, err = grpcPath(&gwapiv1.GRPCMethodMatch{Type: ptrOf(gwapiv1.GRPCMethodMatchRegularExpression),
		Method: new("a^b")})
	require.ErrorIs(t, err, errAnchorPlacement)
}

// translateReport translates a fixture and returns the model with its status report
func translateReport(t *testing.T, name string) (*ir.IR, *ir.Report) {
	t.Helper()
	c := load(t, filepath.Join("testdata", name+".yaml"))
	model, report, _ := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	return model, report
}

func ptrOf[T any](v T) *T { return new(v) }

// originAddr names the origin a request reached by the address it was served on, since every
// origin sees the client's own Host
func originAddr(r *http.Request) string {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return ""
	}
	return addr.String()
}

func TestGRPCRouteIsServed(t *testing.T) {
	// gRPC calls are POSTs to /{service}/{method}: each match shape routes the calls it
	// names to the origin, with the client's request for trailers and the route's header
	// filter, and nothing else on the hostname is served
	var mu sync.Mutex
	var seen []string
	rtr := serveGeneratedWith(t, "grpc", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+"|"+r.Header.Get("X-Route")+"|"+r.Header.Get("Te")+
			"|"+r.Proto)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		_, _ = w.Write([]byte("reply"))
		w.Header().Set("Grpc-Status", "0")
	})
	post := func(path string, headers ...string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("call"))
		req.Host = "rpc.example.com"
		req.Header.Set("Content-Type", "application/grpc")
		req.Header.Set("Te", "trailers")
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		rtr.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, http.StatusOK, post("/shop.v1.Orders/List"))
	require.Equal(t, http.StatusOK, post("/shop.v1.Carts/GetCart", "x-tenant", "gold"))
	require.NotEqual(t, http.StatusOK, post("/shop.v1.Carts/GetCart"), "the header match is required")
	require.Equal(t, http.StatusOK, post("/any.Service/Health"))
	require.Equal(t, http.StatusOK, post("/shop.v2.Users/GetUser"))
	require.NotEqual(t, http.StatusOK, post("/shop.v2.Users/DeleteUser"))
	require.NotEqual(t, http.StatusOK, post("/shop.v1.Payments/Charge"))
	status, _ := request(rtr, http.MethodGet, "rpc.example.com", "/shop.v1.Orders/List")
	require.NotEqual(t, http.StatusOK, status, "a gRPC call is a POST")
	status, _ = request(rtr, http.MethodGet, "rpc.example.com", "/")
	require.NotEqual(t, http.StatusOK, status, "the conflicting HTTPRoute is not served")
	status, body := request(rtr, http.MethodGet, "shop.example.com", "/")
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "reply", body)
	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, seen, "/shop.v1.Orders/List|rpc|trailers|HTTP/2.0",
		"the origin is reached over HTTP/2 with the filter's header and the trailer request")
}

func TestTranslateEndpointMirrors(t *testing.T) {
	// In endpoint mode every mirror is an ALB over a template of its own, so two mirrors in
	// one group keep their own TLS settings rather than overwriting each other's template
	require.Empty(t, golden(t, "endpoint-mirror"))
	model, _, o := translateFixture(t, "endpoint-mirror")
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	conf := decodeOverlay(t, overlay)
	audit := conf.Backends["kgw--httproute.shop.split_r0_mirror"]
	replay := conf.Backends["kgw--httproute.shop.split_r0_b1_mirror"]
	require.NotNil(t, audit)
	require.NotNil(t, replay)
	auditTmpl := audit.ALBOptions.Discovery.TemplateBackend
	replayTmpl := replay.ALBOptions.Discovery.TemplateBackend
	require.NotEqual(t, auditTmpl, replayTmpl, "each mirror has its own template")
	require.Equal(t, "audit.internal", conf.Backends[auditTmpl].TLS.ServerName)
	require.True(t, conf.Backends[auditTmpl].TLS.ExcludeSystemRoots)
	require.Equal(t, "replay.internal", conf.Backends[replayTmpl].TLS.ServerName)
	require.False(t, conf.Backends[replayTmpl].TLS.ExcludeSystemRoots)
	require.Len(t, conf.Backends["kgw--httproute.shop.split_r0"].Paths[0].Mirrors, 1)
	require.Len(t, conf.Backends["kgw--httproute.shop.split_r0_b1"].Paths[0].Mirrors, 1)
	require.Empty(t, conf.Backends["kgw--httproute.shop.split_r0_b0"].Paths[0].Mirrors)
}

func decodeOverlay(t *testing.T, overlay *config.Overlay) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(baseConfig), 0o600))
	conf, err := config.LoadWithOverlay([]string{"-config", path}, overlay)
	require.NoError(t, err)
	return conf
}

func TestBothMirrorsAreServed(t *testing.T) {
	// A rule served by one backendRef that carries a rule mirror and a reference mirror
	// sends one copy to each mirror Service beside the request the backend answers
	var mu sync.Mutex
	hosts := make(map[string]int)
	rtr := serveGeneratedWith(t, "mirror", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts[originAddr(r)]++
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	status, _ := request(rtr, http.MethodGet, "shop.example.com", "/both")
	require.Equal(t, http.StatusOK, status)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(hosts) == 3
	}, 5*time.Second, 10*time.Millisecond, "the request reaches the origin and both mirrors")
	mu.Lock()
	defer mu.Unlock()
	for host, n := range hosts {
		require.Equal(t, 1, n, host)
	}
}

func TestGRPCMethodMatchOutranksFallback(t *testing.T) {
	// A method-only match is a pattern, and a rule naming no method is the pattern catch-all,
	// so the specific method reaches its Service and everything else the fallback
	containing(t, golden(t, "grpc-fallback"), "GRPCRoute/shop/unsupported",
		"must be at the expression's start or end; the route is not served")
	var mu sync.Mutex
	var hosts []string
	rtr := serveGeneratedWith(t, "grpc-fallback", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, originAddr(r))
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	post := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("call"))
		req.Host = "rpc.example.com"
		w := httptest.NewRecorder()
		rtr.ServeHTTP(w, req)
		return w.Code
	}
	// the route whose expression cannot be honored is refused whole and serves nothing
	req := httptest.NewRequest(http.MethodPost, "/foofoo/Check", strings.NewReader("call"))
	req.Host = "refused.example.com"
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusOK, w.Code)
	_, report := translateReport(t, "grpc-fallback")
	for _, r := range report.Routes {
		accepted := condOf(t, r.Parents[0].Conditions, "Accepted")
		require.Equal(t, r.Source.Name != "unsupported", accepted.Status, r.Source.Name)
		if r.Source.Name == "unsupported" {
			require.EqualValues(t, gwapiv1.RouteReasonUnsupportedValue, accepted.Reason)
			require.Contains(t, accepted.Message, "rule 0 match 0: service \"(?:^foo)+\"")
		}
	}

	require.Equal(t, http.StatusOK, post("/example.Health/Check"))
	require.Equal(t, http.StatusOK, post("/example.Orders/List"))
	require.Equal(t, http.StatusOK, post("/other.Health/Check"))
	mu.Lock()
	require.Len(t, hosts, 3)
	require.Equal(t, hosts[0], hosts[2], "every Check reaches the health Service")
	require.NotEqual(t, hosts[0], hosts[1], "the fallback reaches the other Service")
	hosts = nil
	mu.Unlock()

	// alternatives and anchors stay within their segment: each call lands on the pattern
	// Service or the fallback as the segment constraints say
	call := func(path string) string {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("call"))
		req.Host = "patterns.example.com"
		w := httptest.NewRecorder()
		rtr.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, path)
		mu.Lock()
		defer mu.Unlock()
		return hosts[len(hosts)-1]
	}
	pattern := call("/foo/Check")
	fallback := call("/foo/Other")
	require.NotEqual(t, pattern, fallback)
	require.Equal(t, pattern, call("/bar/Check"), "the second alternative carries the method")
	require.Equal(t, fallback, call("/bar/Other"), "the first alternative carries the method too")
	require.Equal(t, pattern, call("/baz/Get"), "a segment's own anchors are honored")
	require.Equal(t, pattern, call("/baz/List"))
	require.Equal(t, fallback, call("/xbaz/Get"), "an anchored service matches the whole segment")
	require.Equal(t, fallback, call("/baz/Gets"))
	require.Equal(t, pattern, call("/qux/Check"), "an anchor inside a group is the segment's")
	require.Equal(t, pattern, call("/quux/Check"), "an anchor on each alternative is the segment's")
	require.Equal(t, fallback, call("/qux/Other"))
	require.Equal(t, fallback, call("/xqux/Check"))
	require.Equal(t, fallback, call("/quux/Checks"))
}

func TestGRPCSegment(t *testing.T) {
	// an anchor-free expression is grouped as written; one anchored at its edges is rewritten
	// so the anchors hold against the segment the group spans; one anchored anywhere else,
	// where the anchor constrains rather than restates the edge, is refused
	segment := func(expr string) string {
		out, err := grpcSegment(expr)
		require.NoError(t, err, expr)
		return out
	}
	for expr, want := range map[string]string{
		"":        anySegment,
		"foo|bar": "(?:foo|bar)",
		"Get.*":   "(?:Get.*)",
		`price\$`: `(?:price\$)`,
		"(bad":    "(?:(bad)",
	} {
		require.Equal(t, want, segment(expr), expr)
	}
	for expr, tc := range map[string]struct{ match, miss []string }{
		"^foo$":         {[]string{"foo"}, []string{"xfoo", "foox"}},
		"(^foo$)":       {[]string{"foo"}, []string{"xfoo", "foox"}},
		"^foo$|^bar$":   {[]string{"foo", "bar"}, []string{"foobar", "xbar"}},
		`\Afoo\z`:       {[]string{"foo"}, []string{"xfoo"}},
		"(?m)^foo$":     {[]string{"foo"}, []string{"xfoo"}},
		"^shop\\.v1":    {[]string{"shop.v1"}, []string{"shopxv1", "xshop.v1"}},
		"Get.*":         {[]string{"Get", "GetUser"}, []string{"Set"}},
		"(?i)get|^list": {[]string{"GET", "list"}, []string{"gets", "xlist"}},
		"(?:^foo)?bar":  {[]string{"foobar", "bar"}, []string{"xfoobar"}},
		"^^foo$$":       {[]string{"foo"}, []string{"xfoo"}},
		`^\bfoo$`:       {[]string{"foo"}, []string{"xfoo"}},
		"(^foo)bar":     {[]string{"foobar"}, []string{"xfoobar", "foo"}},
	} {
		re := regexp.MustCompile("^/" + segment(expr) + "/x$")
		for _, m := range tc.match {
			require.True(t, re.MatchString("/"+m+"/x"), "%s should match %s as %s", expr, m, re)
		}
		for _, m := range tc.miss {
			require.False(t, re.MatchString("/"+m+"/x"), "%s should not match %s as %s", expr, m, re)
		}
	}
	for _, expr := range []string{
		"(?:^foo)+", "(?:foo$|bar)Baz", "a^b", "a$b", "(?:^foo){2}", "(?:^foo)*", "x(?:^a|b)",
		"(?:a|b$)x",
	} {
		_, err := grpcSegment(expr)
		require.ErrorIs(t, err, errAnchorPlacement, expr)
	}
}

func BenchmarkRefuseCrossKindConflicts(b *testing.B) {
	// the pass over accepted routes must not scan every pair: HTTP-only sets are skipped and
	// mixed sets consult only the other kind on the same listener
	for _, mixed := range []bool{false, true} {
		name := "http-only"
		if mixed {
			name = "mixed"
		}
		b.Run(name, func(b *testing.B) {
			t := &translator{problems: translate.NewProblems(true)}
			plans := make([]*routePlan, 0, 2000)
			listeners := []*listenerState{{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"}}
			for i := range 2000 {
				kind := ir.KindHTTPRoute
				if mixed && i%2 == 1 {
					kind = ir.KindGRPCRoute
				}
				p := &routePlan{src: ir.Source{Kind: kind, Namespace: "ns", Name: fmt.Sprint("r", i)},
					report: newRouteReport(ir.Source{})}
				// both kinds share every listener, each route on its own hostname, so nothing conflicts
				l := listeners[(i/2)%len(listeners)]
				p.attachments = []attachment{{listener: l, hostnames: []string{fmt.Sprintf("h%d.example.com", i)}, plan: p}}
				plans = append(plans, p)
			}
			b.ReportAllocs()
			for b.Loop() {
				kept := t.refuseCrossKindConflicts(slices.Clone(plans))
				if len(kept) != len(plans) {
					b.Fatal("a nonconflicting route was refused")
				}
			}
		})
	}
}

func TestCrossKindConflictIndex(t *testing.T) {
	// the index finds a conflict through an exact hostname, a wildcard on either side, and an
	// unrestricted attachment, and names the oldest of several
	tr := &translator{problems: translate.NewProblems(true)}
	l := &listenerState{name: "l"}
	newPlan := func(rank int, kind string, hosts ...string) *routePlan {
		p := &routePlan{rank: rank, src: ir.Source{Kind: kind, Namespace: "ns", Name: fmt.Sprint(kind, rank)},
			report: newRouteReport(ir.Source{})}
		p.attachments = []attachment{{listener: l, hostnames: hosts, plan: p}}
		l.attached++
		return p
	}
	plans := []*routePlan{
		newPlan(0, ir.KindHTTPRoute, "a.example.com"),
		newPlan(1, ir.KindHTTPRoute, "*.wild.example.com"),
		newPlan(2, ir.KindGRPCRoute, "b.example.com"),          // kept: no overlap
		newPlan(3, ir.KindGRPCRoute, "a.example.com"),          // exact conflict with 0
		newPlan(4, ir.KindGRPCRoute, "x.wild.example.com"),     // exact under the kept wildcard 1
		newPlan(5, ir.KindGRPCRoute, "*.example.com"),          // wildcard covering exact 0 and wildcard 1; oldest is 0
		newPlan(6, ir.KindHTTPRoute),                           // unrestricted: conflicts with kept gRPC 2
		newPlan(7, ir.KindGRPCRoute, "c.example.com", "z.org"), // kept
		newPlan(8, ir.KindHTTPRoute, "z.org"),                  // exact conflict with 7
	}
	kept := tr.refuseCrossKindConflicts(plans)
	var names []string
	for _, p := range kept {
		names = append(names, p.src.Name)
	}
	require.Equal(t, []string{"HTTPRoute0", "HTTPRoute1", "GRPCRoute2", "GRPCRoute7"}, names)
	require.Equal(t, 4, l.attached)
	containing(t, tr.problems.List(), "GRPCRoute/ns/GRPCRoute5", "the hostnames of HTTPRoute/ns/HTTPRoute0")
	containing(t, tr.problems.List(), "GRPCRoute/ns/GRPCRoute4", "the hostnames of HTTPRoute/ns/HTTPRoute1")
	containing(t, tr.problems.List(), "HTTPRoute/ns/HTTPRoute6", "the hostnames of GRPCRoute/ns/GRPCRoute2")
	containing(t, tr.problems.List(), "HTTPRoute/ns/HTTPRoute8", "the hostnames of GRPCRoute/ns/GRPCRoute7")

	// an unrestricted kept attachment conflicts with anything of the other kind
	l2 := &listenerState{name: "l2"}
	open := &routePlan{rank: 0, src: ir.Source{Kind: ir.KindGRPCRoute, Name: "open"}, report: newRouteReport(ir.Source{})}
	open.attachments = []attachment{{listener: l2, plan: open}}
	closed := &routePlan{rank: 1, src: ir.Source{Kind: ir.KindHTTPRoute, Name: "closed"}, report: newRouteReport(ir.Source{})}
	closed.attachments = []attachment{{listener: l2, hostnames: []string{"h.example.com"}, plan: closed}}
	require.Len(t, tr.refuseCrossKindConflicts([]*routePlan{open, closed}), 1)
	require.Nil(t, oldest(nil, nil))
	require.Same(t, open, oldest(open, nil))
}

func TestTranslateServicePortShapes(t *testing.T) {
	// a port whose appProtocol says h2c is spoken to by prior knowledge; a headless Service is
	// dialed on its numeric target port, and one naming its target port is refused
	h2c := "kubernetes.io/h2c"
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	svc := c.services["shop/web-svc"]
	svc.Spec.Ports[0].AppProtocol = &h2c
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.Empty(t, problems)
	require.True(t, model.Backends[0].Members[0].Service.H2C)
	require.EqualValues(t, 8080, model.Backends[0].Members[0].Service.Port)
	o, err := compile.Compile(model, options(t))
	require.NoError(t, err)
	require.Contains(t, string(o.Data), "h2c_prior_knowledge: true")

	c = load(t, filepath.Join("testdata", "basic.yaml"))
	svc = c.services["shop/web-svc"]
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.Ports[0].TargetPort = intstr.FromInt32(3000)
	model, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.Empty(t, problems)
	require.EqualValues(t, 3000, model.Backends[0].Members[0].Service.Port,
		"a headless Service resolves to pods listening on the target port")

	// a headless Service naming its target port is reachable only where its pods are discovered:
	// refused under service routing, served under endpoint routing however that mode is selected
	c = load(t, filepath.Join("testdata", "basic.yaml"))
	svc = c.services["shop/web-svc"]
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.Ports[0].TargetPort = intstr.FromString("web")
	_, report, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "HTTPRoute/shop/web",
		`names its target port "web", which service routing cannot resolve`)
	require.EqualValues(t, gwapiv1.RouteReasonUnsupportedValue,
		condOf(t, report.Routes[0].Parents[0].Conditions, "ResolvedRefs").Reason)
	endpoint := func(o *kubecfg.Options) { o.Defaults.RoutingMode = kubecfg.RoutingModeEndpoint }
	model, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t, endpoint),
	})
	require.Empty(t, problems)
	member := model.Backends[0].Members[0]
	require.Equal(t, "web", member.Service.TargetPortName)
	require.EqualValues(t, 8080, member.Service.Port, "the Service port stands, unused")
	o, err = compile.Compile(model, options(t, endpoint))
	require.NoError(t, err)
	require.Contains(t, string(o.Data), "port: http",
		"the endpoint query selects the pods' port by the Service port's name")
	c = load(t, filepath.Join("testdata", "class-params.yaml"))
	c.configMaps["infra/gateway-params"].Data["routing_mode"] = kubecfg.RoutingModeEndpoint
	svc = c.services["shop/web-svc"]
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.Ports[0].TargetPort = intstr.FromString("web")
	model, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t), KnownNames: known,
	})
	require.Empty(t, problems, "a class parameter selecting endpoint routing serves it")
	require.Equal(t, "web", model.Backends[0].Members[0].Service.TargetPortName)

	// a mirror to such a Service is dropped under service routing as an unresolved one is
	c = load(t, filepath.Join("testdata", "mirror.yaml"))
	svc = c.services["shop/shadow-svc"]
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.Ports[0].TargetPort = intstr.FromString("web")
	model, report, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	containing(t, problems, "HTTPRoute/shop/shadowed",
		"requestMirror: headless service shop/shadow-svc names its target port \"web\"")
	require.Empty(t, model.Routes[0].Rules[0].Filters, "the rule's mirror is dropped")
	for _, g := range model.Backends {
		if g.RuleIndex == 1 {
			require.Empty(t, g.Members[1].Filters, "the member's mirror is dropped")
		}
	}
	require.False(t, condOf(t, report.Routes[0].Parents[0].Conditions, "ResolvedRefs").Status)
}

func TestTranslateCertificateIdentityConflicts(t *testing.T) {
	// One store serves a port and answers for a name with the first certificate carrying it, so a
	// listener bringing a different certificate carrying a name already answered for is refused
	set := func(c *cache, secret string, names ...string) {
		key, crt, err := tlstest.GetTestKeyAndCertWithNames(names...)
		require.NoError(t, err)
		c.secrets[secret].Data = map[string][]byte{
			corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key}
	}
	listener := func(report *ir.Report, gateway, section string) ir.ListenerStatus {
		t.Helper()
		for _, g := range report.Gateways {
			if g.Source.Name != gateway {
				continue
			}
			for _, l := range g.Listeners {
				if l.Name == section {
					return l
				}
			}
		}
		t.Fatalf("no listener %s/%s in the report", gateway, section)
		return ir.ListenerStatus{}
	}
	refused := func(report *ir.Report, gateway, section, fragment string) {
		t.Helper()
		l := listener(report, gateway, section)
		c := condOf(t, l.Conditions, "Conflicted")
		require.True(t, c.Status, "%s/%s: %v", gateway, section, l.Conditions)
		require.EqualValues(t, gwapiv1.ListenerReasonHostnameConflict, c.Reason)
		require.Contains(t, c.Message, fragment)
		require.False(t, condOf(t, l.Conditions, "Accepted").Status)
	}
	accepted := func(report *ir.Report, gateway, section string) {
		t.Helper()
		l := listener(report, gateway, section)
		require.True(t, condOf(t, l.Conditions, "Accepted").Status, "%s/%s: %v", gateway, section,
			l.Conditions)
		require.False(t, condOf(t, l.Conditions, "Conflicted").Status)
	}

	// a hostless listener brings a certificate carrying a name an older Gateway's already
	// answers for; rotations end and begin the overlap, and identical material is one certificate
	c := load(t, filepath.Join("testdata", "https-hostnames.yaml"))
	set(c, "infra/a-tls", "a.example.com", "b.example.com")
	set(c, "infra/b-tls", "b.example.com")
	report, problems := reportOf(t, c)
	accepted(report, "older", "https")
	accepted(report, "same", "https")
	refused(report, "other", "hostless", `certificate infra/b-tls answers for "b.example.com" on `+
		`port 443, which listener "https" of Gateway/infra/older already answers for from `+
		`certificate infra/a-tls`)
	containing(t, problems, "Gateway/infra/other", `listener "hostless"`, `answers for "b.example.com"`)
	set(c, "infra/b-tls", "c.example.com")
	report, _ = reportOf(t, c)
	accepted(report, "other", "hostless")
	set(c, "infra/a-tls", "a.example.com", "c.example.com")
	report, _ = reportOf(t, c)
	refused(report, "other", "hostless", `"c.example.com"`)
	key, crt, err := tlstest.GetTestKeyAndCertWithNames("a.example.com", "*.example.com")
	require.NoError(t, err)
	for _, name := range []string{"infra/a-tls", "infra/b-tls"} {
		c.secrets[name].Data = map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key}
	}
	report, _ = reportOf(t, c)
	accepted(report, "other", "hostless")
	// a wildcard is a name of its own
	set(c, "infra/b-tls", "*.example.com")
	report, _ = reportOf(t, c)
	refused(report, "other", "hostless", `"*.example.com"`)

	// a store keeps serving a certificate whose Secret rotates to unusable material, so its names
	// stay claimed on that port until a usable rotation, and on a port never holding it, nothing is
	c = load(t, filepath.Join("testdata", "https-hostnames.yaml"))
	last := make(map[string]ir.CertIdentity)
	remembering := func(sec *corev1.Secret) (ir.CertIdentity, error) {
		id, err := ir.ParseCertPair(sec.Data[corev1.TLSCertKey], sec.Data[corev1.TLSPrivateKeyKey])
		if err == nil {
			last[sec.Namespace+"/"+sec.Name] = id
		}
		return id, err
	}
	serving := func(l ir.Listener, key string) (ir.CertIdentity, bool) {
		if l.Port != 443 {
			return ir.CertIdentity{}, false
		}
		id, ok := last[key]
		return id, ok
	}
	judged := func() *ir.Report {
		_, report, _ := Translate(Config{
			Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
			CertJudge: remembering, CertServing: serving,
		})
		return report
	}
	set(c, "infra/a-tls", "a.example.com", "b.example.com")
	set(c, "infra/b-tls", "c.example.com")
	report = judged()
	accepted(report, "older", "https")
	accepted(report, "other", "hostless")
	c.secrets["infra/a-tls"].Data[corev1.TLSCertKey] = []byte("garbage")
	report = judged()
	accepted(report, "older", "https")
	require.False(t, condOf(t, listener(report, "older", "https").Conditions, "ResolvedRefs").Status)
	set(c, "infra/b-tls", "b.example.com")
	report = judged()
	refused(report, "other", "hostless", `"b.example.com"`)
	set(c, "infra/a-tls", "a.example.com")
	report = judged()
	accepted(report, "other", "hostless")
	c.secrets["infra/a-tls"].Data[corev1.TLSCertKey] = []byte("garbage")
	report = judged()
	accepted(report, "other", "hostless")
	set(c, "infra/a-tls", "a.example.com", "b.example.com")
	report = judged()
	refused(report, "other", "hostless", `"b.example.com"`)
	c.secrets["infra/a-tls"].Data[corev1.TLSCertKey] = []byte("garbage")
	for _, g := range c.gateways {
		i := 0
		if g.Name == "other" {
			i = 1
		}
		if g.Name == "older" || g.Name == "other" {
			l := g.Spec.Listeners[i]
			l.Name, l.Port, l.Hostname = "alt", 8443, nil
			g.Spec.Listeners = append(g.Spec.Listeners, l)
		}
	}
	report = judged()
	refused(report, "other", "hostless", `"b.example.com"`)
	accepted(report, "older", "alt")
	accepted(report, "other", "alt")
	// without that memory, unusable material answers for nothing
	report, _ = reportOf(t, c)
	accepted(report, "other", "hostless")

	// a listener declaring a hostname of its own collides the same way when the older
	// certificate carries that hostname
	c = load(t, filepath.Join("testdata", "https-hostnames.yaml"))
	set(c, "infra/a-tls", "a.example.com", "b.example.com")
	set(c, "infra/b-tls", "b.example.com")
	for _, g := range c.gateways {
		if g.Name == "other" {
			g.Spec.Listeners[0].Hostname = ptrOf(gwapiv1.Hostname("b.example.com"))
		}
	}
	report, _ = reportOf(t, c)
	refused(report, "other", "other-cert", `listener "https" of Gateway/infra/older`)

	// and so does a later listener of the same Gateway
	c = load(t, filepath.Join("testdata", "https-hostnames.yaml"))
	set(c, "infra/a-tls", "a.example.com", "b.example.com")
	set(c, "infra/b-tls", "b.example.com")
	for _, g := range c.gateways {
		if g.Name == "older" {
			l := g.Spec.Listeners[0]
			tls := *l.TLS
			tls.CertificateRefs = []gwapiv1.SecretObjectReference{{Name: "b-tls"}}
			l.Name, l.Hostname, l.TLS = "second", ptrOf(gwapiv1.Hostname("b.example.com")), &tls
			g.Spec.Listeners = append(g.Spec.Listeners, l)
		}
	}
	report, _ = reportOf(t, c)
	accepted(report, "older", "https")
	refused(report, "older", "second", `listener "https" of Gateway/infra/older`)
}

func TestTranslateGatewayInfrastructureParameters(t *testing.T) {
	// nothing reads a Gateway's infrastructure parameters, so a Gateway naming some is refused
	// with InvalidParameters and its routes are told, as for a refused GatewayClass
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.gateways[0].Spec.Infrastructure = &gwapiv1.GatewayInfrastructure{
		ParametersRef: &gwapiv1.LocalParametersReference{
			Group: "invalid.io", Kind: "InvalidParameters", Name: "invalid"}}
	model, report, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.Empty(t, model.Listeners, "the gateway is not served")
	accepted := condOf(t, report.Gateways[0].Conditions, "Accepted")
	require.False(t, accepted.Status)
	require.EqualValues(t, gwapiv1.GatewayReasonInvalidParameters, accepted.Reason)
	require.Contains(t, accepted.Message, "invalid.io/InvalidParameters")
	require.False(t, condOf(t, report.Gateways[0].Conditions, "Programmed").Status)
	require.EqualValues(t, gwapiv1.RouteReasonNoMatchingParent,
		condOf(t, report.Routes[0].Parents[0].Conditions, "Accepted").Reason)
	containing(t, problems, "Gateway/infra/gw", "spec.infrastructure.parametersRef")
}
