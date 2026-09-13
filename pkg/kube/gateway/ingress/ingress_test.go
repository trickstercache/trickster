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

package ingress

import (
	"flag"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	cacheregistry "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/annotations"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/compile"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
)

var update = flag.Bool("update", false, "rewrite the golden overlay files")

// controllerName is what the fixture IngressClasses name
const controllerName = kubecfg.DefaultGatewayClassControllerName

// cache is a Cache built from decoded fixture objects
type cache struct {
	ingresses []*netv1.Ingress
	classes   []*netv1.IngressClass
	services  map[string]*corev1.Service
	secrets   map[string]*corev1.Secret
	policies  []*cachepolicy.CachePolicy
}

// exists reports whether a policy target is among the fixture objects
func (c *cache) exists(kind, ns, name string) bool {
	switch kind {
	case cachepolicy.KindIngress:
		return slices.ContainsFunc(c.ingresses, func(i *netv1.Ingress) bool {
			return i.Namespace == ns && i.Name == name
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
	if len(c.policies) == 0 {
		return nil
	}
	return cachepolicy.New(c.policies, cachepolicy.Config{
		Known:  ir.ConfiguredNames{Caches: sets.New([]string{"objects"})},
		Exists: c.exists, ProviderPaths: prometheusPathNames,
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

func newCache() *cache {
	return &cache{
		services: make(map[string]*corev1.Service),
		secrets:  make(map[string]*corev1.Secret),
	}
}

func (c *cache) Ingresses() []*netv1.Ingress           { return c.ingresses }
func (c *cache) IngressClasses() []*netv1.IngressClass { return c.classes }

func (c *cache) Service(ns, name string) *corev1.Service {
	return c.services[ns+"/"+name]
}

func (c *cache) Secret(ns, name string) *corev1.Secret {
	// the watcher only holds TLS secrets, so the fixture cache does not
	// return anything else either
	s := c.secrets[ns+"/"+name]
	if s == nil || s.Type != corev1.SecretTypeTLS {
		return nil
	}
	return s
}

func load(t *testing.T, path string) *cache {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	c := newCache()
	decode := scheme.Codecs.UniversalDeserializer().Decode
	for doc := range strings.SplitSeq(string(data), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		if strings.Contains(doc, "kind: "+cachepolicy.Kind) {
			// the resource is not in any scheme, so it is read the way the watcher reads it
			c.policies = append(c.policies, decodePolicy(t, doc))
			continue
		}
		obj, _, err := decode([]byte(doc), nil, nil)
		require.NoError(t, err, "decoding %s", path)
		switch o := obj.(type) {
		case *netv1.Ingress:
			c.ingresses = append(c.ingresses, o)
		case *netv1.IngressClass:
			c.classes = append(c.classes, o)
		case *corev1.Service:
			c.services[o.Namespace+"/"+o.Name] = o
		case *corev1.Secret:
			c.secrets[o.Namespace+"/"+o.Name] = o
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

func translateFixture(t *testing.T, name string,
	mutate ...func(*kubecfg.Options),
) (*ir.IR, []ir.Problem, *kubecfg.Options) {
	t.Helper()
	o := options(t, mutate...)
	c := load(t, filepath.Join("testdata", name+".yaml"))
	idx := policyIndex(c)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, o.IngressClass), Options: o, Policies: idx,
	})
	// the controller reports the index's problems alongside the translator's
	return model, append(problems, idx.Problems()...), o
}

func golden(t *testing.T, name string, mutate ...func(*kubecfg.Options)) []ir.Problem {
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

func TestTranslateBasic(t *testing.T) {
	require.Empty(t, golden(t, "basic"))
}

func TestTranslatePathTypes(t *testing.T) {
	require.Empty(t, golden(t, "paths"))
}

func TestTranslateDefaultBackend(t *testing.T) {
	require.Empty(t, golden(t, "default-backend"))
}

func TestTranslateTLS(t *testing.T) {
	require.Empty(t, golden(t, "tls"))
}

func TestTranslateAnnotations(t *testing.T) {
	require.Empty(t, golden(t, "annotations"))
}

func TestTranslateRegexRewrite(t *testing.T) {
	require.Empty(t, golden(t, "regex"))
}

func TestTranslateMultiHost(t *testing.T) {
	require.Empty(t, golden(t, "multi-host"))
}

func TestTranslateConflict(t *testing.T) {
	// TestTranslateConflict pins the tie-break: the older Ingress keeps the
	// host and path, and the newer one is told why it lost
	problems := golden(t, "conflict")
	require.Len(t, problems, 1)
	require.Equal(t, "Ingress/shop/newer", problems[0].Source.Key())
	require.Contains(t, problems[0].Detail, "already served by Ingress/shop/older")
}

func TestTranslateUnresolvedBackend(t *testing.T) {
	// TestTranslateUnresolvedBackend keeps a route that cannot reach its
	// Service answering, rather than disappearing
	problems := golden(t, "unresolved")
	require.Len(t, problems, 2)
	details := []string{problems[0].Detail, problems[1].Detail}
	require.Contains(t, details, "service shop/missing-svc not found")
	require.Contains(t, details, "service shop/web-svc has no port 9999")
}

// baseConfig is a minimal file configuration the generated overlay is merged onto; it defines
// the cache the annotation fixture names, since the controller cannot define one
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
    "502": 5s
`

func TestGeneratedOverlayLoadsAndValidates(t *testing.T) {
	// The overlay has to survive the real loader, not just a decode: names, cross-references
	// and every option default are only checked there
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(baseConfig), 0o600))
	for _, name := range goldenFixtures(t) {
		t.Run(name, func(t *testing.T) {
			model, _, o := translateFixture(t, name)
			overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
			require.NoError(t, err)
			conf, err := config.LoadWithOverlay([]string{"-config", path}, overlay)
			require.NoError(t, err)
			require.NoError(t, conf.Backends.Validate())
			require.NoError(t, conf.Caches.Validate())
			require.NoError(t, validate.Validate(conf))
		})
	}
}

func goldenFixtures(t *testing.T) []string {
	// goldenFixtures lists every fixture that has a golden overlay
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("testdata", "*.golden.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, matches)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimSuffix(filepath.Base(m), ".golden.yaml"))
	}
	return out
}

func TestTranslateTLSCertRefs(t *testing.T) {
	// The certificates never reach the overlay, so the IR's cert refs are the only record of
	// what the controller must push; which listener can serve one is the listener's own business
	model, problems, _ := translateFixture(t, "tls", func(o *kubecfg.Options) {
		o.Ingress = &kubecfg.IngressOptions{
			ListenerNames: []string{"web", "websecure"}}
	})
	require.Empty(t, problems)
	require.Len(t, model.Certs, 1)
	require.Equal(t, "shop/shop-tls", model.Certs[0].Name)
	require.Equal(t, "shop/shop-tls", model.Certs[0].Key())
	require.Len(t, model.Listeners, 2)
	for _, l := range model.Listeners {
		require.Equal(t, []string{"shop/shop-tls"}, l.CertRefs,
			"listener %q must offer the certificate it may be able to serve",
			l.Name)
	}
}

func TestTranslateWithoutCerts(t *testing.T) {
	// An Ingress with no TLS block leaves the listeners with nothing to serve
	model, _, _ := translateFixture(t, "basic")
	require.Empty(t, model.Certs)
	for _, l := range model.Listeners {
		require.Empty(t, l.CertRefs)
	}
}

func TestIngressListenersAreConfigured(t *testing.T) {
	// The listeners claimed Ingresses are served on are the operator's, named the way a backend
	// names them; the controller defines none and every claimed route attaches to all
	model, _, _ := translateFixture(t, "tls", func(o *kubecfg.Options) {
		o.Ingress = &kubecfg.IngressOptions{
			ListenerNames: []string{"web", "websecure"}}
	})
	require.Len(t, model.Listeners, 2)
	for _, l := range model.Listeners {
		require.True(t, l.External,
			"a configured listener is not the controller's to define")
		require.Zero(t, l.Port)
		require.Empty(t, l.Protocol)
	}
	require.Equal(t, []string{"web", "websecure"},
		[]string{model.Listeners[0].Name, model.Listeners[1].Name})
	for _, r := range model.Routes {
		require.Equal(t, []string{"web", "websecure"}, r.Listeners)
	}
}

func TestIngressDefaultsToTheDefaultFrontend(t *testing.T) {
	// Naming no listener serves claimed Ingresses on the default frontend,
	// which is where a backend that names none is served
	model, _, _ := translateFixture(t, "basic")
	require.Len(t, model.Listeners, 1)
	require.Equal(t, listenerconfig.DefaultFrontendName, model.Listeners[0].Name)
	require.True(t, model.Listeners[0].External)
}

func TestTranslateIgnoresUnclaimed(t *testing.T) {
	// An Ingress this controller does not claim is translated into nothing at
	// all, not merely left unstatused
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	c.classes[0].Spec.Controller = "someone.else/controller"
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.True(t, model.IsEmpty())
	require.Empty(t, problems)
}

func TestTranslateNilConfig(t *testing.T) {
	model, _, problems := Translate(Config{})
	require.True(t, model.IsEmpty())
	require.Empty(t, problems)
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: ""},
		{in: "  Shop.Example.COM ", want: "shop.example.com"},
		{in: "*.example.com", want: "*.example.com"},
		{in: "a.*.example.com", wantErr: true},
		{in: "*.*.example.com", wantErr: true},
		{in: "*", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			got, err := translate.Hostname(test.in, translate.HostnameAllowEmpty)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestTranslateRewritePrefix(t *testing.T) {
	// A prefix match knows only the segments it matched, so its rewrite
	// replaces just those and carries the rest of the path through
	require.Empty(t, golden(t, "rewrite-prefix"))
}

func ingressWith(host, path string, pathType *netv1.PathType,
	backend netv1.IngressBackend, mutate ...func(*netv1.Ingress),
) *netv1.Ingress {
	// ingressWith builds a claimed Ingress with one rule and one path
	className := "trickster"
	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"},
		Spec: netv1.IngressSpec{
			IngressClassName: &className,
			Rules: []netv1.IngressRule{{
				Host: host,
				IngressRuleValue: netv1.IngressRuleValue{
					HTTP: &netv1.HTTPIngressRuleValue{
						Paths: []netv1.HTTPIngressPath{{
							Path: path, PathType: pathType, Backend: backend,
						}},
					},
				},
			}},
		},
	}
	for _, m := range mutate {
		m(ing)
	}
	return ing
}

func svcBackend(name string, port int32) netv1.IngressBackend {
	// svcBackend references the fixture Service by port number
	return netv1.IngressBackend{Service: &netv1.IngressServiceBackend{
		Name: name, Port: netv1.ServiceBackendPort{Number: port}}}
}

func withCache(ings ...*netv1.Ingress) *cache {
	// withCache returns a cache holding the default IngressClass, one Service
	// and the supplied Ingresses
	c := newCache()
	c.classes = []*netv1.IngressClass{{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "trickster",
			Annotations: map[string]string{class.DefaultClassAnnotation: "true"},
		},
		Spec: netv1.IngressClassSpec{Controller: controllerName},
	}}
	c.services["shop/web-svc"] = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-svc"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "http", Port: 8080}}},
	}
	c.ingresses = ings
	return c
}

func run(t *testing.T, c *cache) (*ir.IR, []ir.Problem) {
	// run translates a cache with the standard options
	t.Helper()
	o := options(t)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: o,
	})
	return model, problems
}

func pathType(t netv1.PathType) *netv1.PathType { return new(t) }

func TestTranslateRejections(t *testing.T) {
	// A piece of an Ingress the translator cannot use is dropped with a reason,
	// and never silently
	prefix := pathType(netv1.PathTypePrefix)
	specific := pathType(netv1.PathTypeImplementationSpecific)
	backend := svcBackend("web-svc", 8080)
	tests := []struct {
		name    string
		ingress *netv1.Ingress
		detail  string
	}{
		{"empty path", ingressWith("a.example.com", "", prefix, backend),
			"path is empty"},
		{"relative path", ingressWith("a.example.com", "api", prefix, backend),
			"must begin with '/'"},
		{"unknown path type", ingressWith("a.example.com", "/api",
			pathType(netv1.PathType("Fuzzy")), backend), "unsupported pathType"},
		{"bad regex", ingressWith("a.example.com", "^/api/(", specific, backend,
			func(i *netv1.Ingress) {
				i.Annotations = map[string]string{
					appinfo.Domain + "/use-regex": "true"}
			}), "not a valid regular expression"},
		{"bad host", ingressWith("*bad.example.com", "/api", prefix, backend),
			"is not routable"},
		{"resource backend", ingressWith("a.example.com", "/api", prefix,
			netv1.IngressBackend{Resource: &corev1.TypedLocalObjectReference{
				Kind: "StorageBucket"}}), "resource reference"},
		{"no backend", ingressWith("a.example.com", "/api", prefix,
			netv1.IngressBackend{}), "names no service"},
		{"unknown port name", ingressWith("a.example.com", "/api", prefix,
			netv1.IngressBackend{Service: &netv1.IngressServiceBackend{
				Name: "web-svc",
				Port: netv1.ServiceBackendPort{Name: "grpc"}}}),
			`has no port "grpc"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, problems := run(t, withCache(test.ingress))
			require.Len(t, problems, 1)
			require.Contains(t, problems[0].Detail, test.detail)
			require.Contains(t, problems[0].String(), "Ingress/shop/web")
		})
	}
}

func TestTranslateSelfConflict(t *testing.T) {
	// An Ingress declaring one path twice is its own conflict, and is told so
	// in its own terms rather than pointed at itself as another owner
	ing := ingressWith("a.example.com", "/api",
		pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080))
	rule := &ing.Spec.Rules[0]
	rule.HTTP.Paths = append(rule.HTTP.Paths, rule.HTTP.Paths[0])
	model, problems := run(t, withCache(ing))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Detail, "declared more than once")
	require.Len(t, model.Backends, 1)
}

func TestTranslateRuleWithoutHTTP(t *testing.T) {
	// A rule with no http block declares nothing to route, which is legal
	ing := ingressWith("a.example.com", "/api",
		pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080))
	ing.Spec.Rules[0].HTTP = nil
	model, problems := run(t, withCache(ing))
	require.Empty(t, problems)
	require.Empty(t, model.Routes)
}

func TestTranslateNilPathType(t *testing.T) {
	// A path with no pathType at all is read the way the API server would
	// default it, and lowered like any other prefix
	ing := ingressWith("a.example.com", "/api", nil, svcBackend("web-svc", 8080))
	model, problems := run(t, withCache(ing))
	require.Empty(t, problems)
	require.Len(t, model.Routes, 1)
	require.Equal(t, []ir.PathMatch{
		{Type: ir.PathPrefix, Value: "/api"},
	}, matchPaths(model.Routes[0].Rules[0]))
}

func matchPaths(rule ir.Rule) []ir.PathMatch {
	// matchPaths is the path predicate of every match of a rule, in order
	out := make([]ir.PathMatch, 0, len(rule.Matches))
	for _, m := range rule.Matches {
		out = append(out, m.Path)
	}
	return out
}

func TestTranslateTLSRejections(t *testing.T) {
	// A TLS block the controller cannot serve is reported rather than producing
	// a listener that fails every handshake
	tests := []struct {
		name   string
		tls    []netv1.IngressTLS
		secret *corev1.Secret
		detail string
	}{
		{"no secret named", []netv1.IngressTLS{{Hosts: []string{"a"}}}, nil,
			"names no secret"},
		{"secret absent", []netv1.IngressTLS{{SecretName: "gone"}}, nil,
			"not found"},
		{"secret not tls", []netv1.IngressTLS{{SecretName: "opaque"}},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "opaque"},
				Type:       corev1.SecretTypeOpaque,
			}, "not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ing := ingressWith("a.example.com", "/api",
				pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080))
			ing.Spec.TLS = test.tls
			c := withCache(ing)
			if test.secret != nil {
				c.secrets[test.secret.Namespace+"/"+test.secret.Name] = test.secret
			}
			model, problems := run(t, c)
			require.Len(t, problems, 1)
			require.Contains(t, problems[0].Detail, test.detail)
			require.Empty(t, model.Certs)
		})
	}
}

func TestTranslateDeduplicatesCertRefs(t *testing.T) {
	// Two Ingresses referencing one Secret produce one certificate, sourced
	// from the older of them
	c := withCache()
	key, crt := tlstest.NamedKeyAndCert("tls")
	c.secrets["shop/tls"] = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: crt, corev1.TLSPrivateKeyKey: key},
	}
	for _, name := range []string{"newer", "older"} {
		ing := ingressWith("a.example.com", "/"+name,
			pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080))
		ing.Name = name
		ing.Spec.TLS = []netv1.IngressTLS{{SecretName: "tls"}}
		c.ingresses = append(c.ingresses, ing)
	}
	c.ingresses[1].CreationTimestamp = metav1.Unix(1, 0)
	c.ingresses[0].CreationTimestamp = metav1.Unix(2, 0)
	model, problems := run(t, c)
	require.Empty(t, problems)
	require.Len(t, model.Certs, 1)
	require.Equal(t, "older", model.Certs[0].Source.Name)
}

func TestTranslateConflictTieBreakIsTotal(t *testing.T) {
	// Two objects created in the same tick still need one answer, or which of
	// them wins would depend on which replica is asked
	c := withCache()
	for _, name := range []string{"beta", "alpha"} {
		ing := ingressWith("a.example.com", "/api",
			pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080))
		ing.Name = name
		ing.CreationTimestamp = metav1.Unix(1, 0)
		c.ingresses = append(c.ingresses, ing)
	}
	model, problems := run(t, c)
	require.Len(t, problems, 1)
	require.Equal(t, "Ingress/shop/beta", problems[0].Source.Key())
	require.Len(t, model.Backends, 1)
	require.Equal(t, "alpha", model.Backends[0].Source.Name)
}

func serveGenerated(t *testing.T, name string,
	mutate ...func(*kubecfg.Options),
) router.Router {
	// serveGenerated loads a fixture's compiled configuration the way the daemon does and returns
	// a router over it; each Service gets its own origin, so a response says which answered
	t.Helper()
	model, _, o := translateFixture(t, name, mutate...)
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(baseConfig), 0o600))
	conf, err := config.LoadWithOverlay([]string{"-config", path}, overlay)
	require.NoError(t, err)
	require.NoError(t, conf.Backends.Validate())
	require.NoError(t, validate.Validate(conf))

	// the compiled origins are in-cluster Service addresses; the routing assertions need
	// reachable ones, one per Service, so that a response says which backend answered
	origins := make(map[string]string)
	for _, b := range conf.Backends {
		if b.OriginURL == "" || !strings.Contains(b.OriginURL, clusterSuffix) {
			continue
		}
		b.OriginURL = originFor(t, origins, b.OriginURL)
		// the origin was already parsed into its parts when the
		// configuration loaded, so redirecting it means redirecting those too
		parsed, err := neturl.Parse(b.OriginURL)
		require.NoError(t, err)
		b.Scheme, b.Host, b.PathPrefix = parsed.Scheme, parsed.Host, parsed.Path
	}
	require.NoError(t, conf.Process())
	clients := make(backends.Backends, len(conf.Backends))
	require.NoError(t, validate.RoutesRulesAndPools(conf, clients))
	caches := cacheregistry.LoadCachesFromConfig(conf)
	t.Cleanup(func() { cacheregistry.CloseCaches(caches) })
	rtr := lm.NewRouter()
	require.NoError(t, routing.RegisterProxyRoutes(conf, clients, rtr, nil,
		caches, nil, false))
	return rtr
}

// clusterSuffix marks a generated in-cluster Service address
const clusterSuffix = ".svc:"

func originFor(t *testing.T, origins map[string]string, clusterURL string) string {
	// originFor returns a server standing in for one in-cluster Service, whose
	// response names the Service and echoes the path it was asked for
	t.Helper()
	if url, ok := origins[clusterURL]; ok {
		return url
	}
	u, err := neturl.Parse(clusterURL)
	require.NoError(t, err)
	service, _, _ := strings.Cut(u.Host, ".")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream-Path", r.URL.Path)
			_, _ = w.Write([]byte(service))
		}))
	t.Cleanup(srv.Close)
	origins[clusterURL] = srv.URL
	return srv.URL
}

func serve(rtr router.Router, host, path string) (int, string) {
	// serve issues one request and returns the status and the body, which names
	// the Service that answered
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestPrefixMatchingIsSegmentWise(t *testing.T) {
	// A Kubernetes Prefix matches whole path elements (/foo matches /foo/bar, never /foobar) and
	// a trailing slash means nothing; the router's prefix tier is a plain string prefix
	rtr := serveGenerated(t, "prefix-semantics")
	tests := []struct {
		path, want string
	}{
		{"/foo", "foo-svc"},
		{"/foo/", "foo-svc"},
		{"/foo/bar", "foo-svc"},
		// a plain string prefix would answer this from /foo
		{"/foobar", "fallback-svc"},
		// declared with a trailing slash, which Kubernetes ignores
		{"/baz", "baz-svc"},
		{"/baz/", "baz-svc"},
		{"/baz/deep", "baz-svc"},
		{"/bazzz", "fallback-svc"},
		// an exact rule matches only itself
		{"/exact", "exact-svc"},
		{"/exact/more", "fallback-svc"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			code, body := serve(rtr, "shop.example.com", test.path)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, test.want, body,
				"%s went to the wrong backend", test.path)
		})
	}
}

func TestExactBeatsPrefixOnTheSamePath(t *testing.T) {
	// Kubernetes gives an exact path type precedence over a prefix matching the same path,
	// whichever object declared it; the prefix keeps everything below that path
	rtr := serveGenerated(t, "exact-over-prefix")
	code, body := serve(rtr, "shop.example.com", "/api")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "exact-svc", body,
		"the exact rule must win the path itself, even though it is newer")
	code, body = serve(rtr, "shop.example.com", "/api/orders")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "prefix-svc", body,
		"the prefix rule must keep everything below the path")

	// losing only the exact half is the precedence working, not a conflict
	_, problems, _ := translateFixture(t, "exact-over-prefix")
	require.Empty(t, problems)
}

func TestDefaultBackendIsTheLastResort(t *testing.T) {
	// A default backend answers whatever no rule matched; a hostless root prefix is not that,
	// since the prefix tier resolves before the regex tier and would take a regex rule's requests
	rtr := serveGenerated(t, "default-backend-regex")
	code, body := serve(rtr, "shop.example.com", "/api/orders")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "api-svc", body,
		"the declared regex route must not be shadowed by the default backend")
	code, body = serve(rtr, "shop.example.com", "/anything/else")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "fallback-svc", body,
		"the default backend must answer what no rule matched")
}

func TestPrefixRewriteFollowsTheMatch(t *testing.T) {
	// A rewrite has to agree with the routing: the exact half replaces the whole
	// path, and the prefix half replaces only the segments it matched
	rtr := serveGenerated(t, "rewrite-prefix")
	tests := []struct{ path, want string }{
		{"/legacy", "/"},
		{"/legacy/orders", "/orders"},
		{"/legacy/orders/1", "/orders/1"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Host = "shop.example.com"
			w := httptest.NewRecorder()
			rtr.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, test.want, w.Header().Get("X-Upstream-Path"))
		})
	}
}

func TestReferencedNamesMustExist(t *testing.T) {
	// A name the running configuration does not define would fail the whole generated
	// configuration; it is rejected as that object's mistake instead, and its route still serves
	known := func() ir.ConfiguredNames {
		return ir.ConfiguredNames{
			Caches:         sets.New([]string{"objects"}),
			NegativeCaches: sets.New([]string{"api-errors"}),
		}
	}
	tests := []struct {
		name, annotation, bad, good, kind string
		get                               func(ir.Policy) string
	}{
		{
			name: "cache", annotation: annotations.CacheName,
			bad: "nonexistent", good: "objects", kind: "cache",
			get: func(p ir.Policy) string { return p.CacheName },
		},
		{
			name: "negative cache", annotation: annotations.NegativeCacheName,
			bad: "nonexistent", good: "api-errors", kind: "negative cache",
			get: func(p ir.Policy) string { return p.NegativeCacheName },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ing := ingressWith("a.example.com", "/api",
				pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080),
				func(i *netv1.Ingress) {
					i.Annotations = map[string]string{
						annotations.Handler: ir.HandlerProxyCache,
						test.annotation:     test.bad,
					}
				})
			cfg := Config{
				Cache: withCache(ing), Claimer: class.New(controllerName, ""),
				Options: options(t), KnownNames: known,
			}
			model, _, problems := Translate(cfg)
			require.Len(t, problems, 1)
			require.Contains(t, problems[0].Detail,
				test.kind+` named "nonexistent"`)
			require.Len(t, model.Routes, 1, "the route must still be served")
			require.Len(t, model.Policies, 1)
			require.Empty(t, test.get(model.Policies[0]),
				"the rejected name must not reach the generated configuration")
			require.Equal(t, ir.HandlerProxyCache, model.Policies[0].Handler,
				"the rest of the object's annotations still apply")

			// a configured name is accepted
			ing.Annotations[test.annotation] = test.good
			model, _, problems = Translate(cfg)
			require.Empty(t, problems)
			require.Equal(t, test.good, test.get(model.Policies[0]))

			// and with nothing to check against, any name is accepted
			ing.Annotations[test.annotation] = "unchecked"
			cfg.KnownNames = nil
			model, _, problems = Translate(cfg)
			require.Empty(t, problems)
			require.Equal(t, "unchecked", test.get(model.Policies[0]))
			cfg.KnownNames = known
		})
	}
}

func TestExplicitRegexRootBeatsTheDefaultBackend(t *testing.T) {
	// An unanchored regular expression is anchored on load, so an explicit hostless "/" rule and
	// the synthesized default backend register one pattern, and map order must not decide
	model, problems, _ := translateFixture(t, "regex-root-vs-default")
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Detail, "default backend is not reachable",
		"the default backend must be reported as the one that lost")

	// exactly one route claims the catch-all pattern
	var claims int
	for _, r := range model.Routes {
		for _, rule := range r.Rules {
			for _, m := range rule.Matches {
				if m.Path.Value == ir.CatchAllRegex {
					claims++
				}
			}
		}
	}
	require.Equal(t, 1, claims,
		"two routes registering one pattern let map order decide the winner")

	// and it is the declared rule, whichever order the router registers in
	rtr := serveGenerated(t, "regex-root-vs-default")
	for _, path := range []string{"/", "/anything", "/deep/path"} {
		code, body := serve(rtr, "shop.example.com", path)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "explicit-svc", body,
			"%s must reach the rule the operator declared", path)
	}
}

func TestRegexPathsAreAnchoredBeforeClaiming(t *testing.T) {
	// A regular expression is claimed in the form the router registers it, so
	// two spellings of one pattern are one route rather than two
	ing := ingressWith("a.example.com", "/api/(.*)",
		pathType(netv1.PathTypeImplementationSpecific),
		svcBackend("web-svc", 8080), func(i *netv1.Ingress) {
			i.Annotations = map[string]string{appinfo.Domain + "/use-regex": "true"}
		})
	model, problems := run(t, withCache(ing))
	require.Empty(t, problems)
	require.Equal(t, []ir.PathMatch{{Type: ir.PathRegex, Value: "^/api/(.*)"}},
		matchPaths(model.Routes[0].Rules[0]))
}

func TestTranslateReportsRejectedAnnotations(t *testing.T) {
	// An annotation the object got wrong is reported against that object while
	// the rest of it still translates
	ing := ingressWith("a.example.com", "/api",
		pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080),
		func(i *netv1.Ingress) {
			i.Annotations = map[string]string{
				appinfo.Domain + "/made-up": "1",
				annotations.Handler:         ir.HandlerProxyCache,
			}
		})
	model, problems := run(t, withCache(ing))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Detail, appinfo.Domain+"/made-up")
	require.Len(t, model.Routes, 1, "the route must still be served")
	require.Equal(t, ir.HandlerProxyCache, model.Policies[0].Handler)
}

func TestTranslateConflictTieBreakAcrossNamespaces(t *testing.T) {
	// Two claimed Ingresses created in the same clock tick still need a total
	// order across namespaces, or which one wins a conflict would vary
	c := withCache()
	c.services["warehouse/web-svc"] = c.services["shop/web-svc"]
	for _, ns := range []string{"warehouse", "shop"} {
		ing := ingressWith("a.example.com", "/api",
			pathType(netv1.PathTypePrefix), svcBackend("web-svc", 8080))
		ing.Namespace = ns
		ing.CreationTimestamp = metav1.Unix(1, 0)
		c.ingresses = append(c.ingresses, ing)
	}
	model, problems := run(t, c)
	require.Len(t, problems, 1)
	require.Equal(t, "Ingress/warehouse/web", problems[0].Source.Key())
	require.Len(t, model.Backends, 1)
	require.Equal(t, "shop", model.Backends[0].Source.Namespace)
}

func TestReportAndProblemReasons(t *testing.T) {
	// The report names every claimed Ingress, and problems carry the reason
	// the Event about them will
	c := load(t, filepath.Join("testdata", "basic.yaml"))
	_, report, _ := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.NotEmpty(t, report.Ingresses)
	for _, src := range report.Ingresses {
		require.Equal(t, ir.KindIngress, src.Kind)
	}

	c.ingresses[0].Annotations = map[string]string{appinfo.Domain + "/bogus": "x"}
	_, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.True(t, slices.ContainsFunc(problems, func(p ir.Problem) bool {
		return p.Reason == ir.ReasonInvalidAnnotation
	}), "%v", problems)

	c = load(t, filepath.Join("testdata", "tls.yaml"))
	c.secrets = map[string]*corev1.Secret{}
	_, _, problems = Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t),
	})
	require.True(t, slices.ContainsFunc(problems, func(p ir.Problem) bool {
		return p.Reason == ir.ReasonInvalidCertificate
	}), "%v", problems)

	_, report, _ = Translate(Config{})
	require.True(t, report.IsEmpty())
}

func TestTranslateCachePolicies(t *testing.T) {
	// A policy on the Ingress is written over its annotations and one on the Service over both;
	// the provider is withheld from the one rule declaring a path it predefines
	problems := golden(t, "cache-policy")
	require.Len(t, problems, 2, problems)
	require.Equal(t, "Ingress/shop/prom", problems[0].Source.Key())
	require.Contains(t, problems[0].Detail, "rule 1: provider \"prometheus\" is not applied")
	require.Contains(t, problems[0].Detail, "/api/v1/query_range")
	require.Equal(t, "TricksterCachePolicy/shop/prom-ingress", problems[1].Source.Key())
	require.Contains(t, problems[1].Detail, "Ingress/shop/prom rule 1")

	model, _, _ := translateFixture(t, "cache-policy")
	policies := make(map[string]string)
	for _, r := range model.Routes {
		for i, rule := range r.Rules {
			policies[r.Source.Name+"/"+strconv.Itoa(i)] = rule.Policy
		}
	}
	const merged = "Ingress/shop/prom+TricksterCachePolicy/shop/prom-ingress"
	require.Equal(t, merged, policies["prom/0"])
	require.Equal(t, merged+"+noprovider", policies["prom/1"])
	require.Empty(t, policies["site/0"])
	var site ir.BackendGroup
	for _, g := range model.Backends {
		if g.Source.Name == "site" {
			site = g
		}
	}
	require.Equal(t, "TricksterCachePolicy/shop/web-service", site.Members[0].Policy)
}
