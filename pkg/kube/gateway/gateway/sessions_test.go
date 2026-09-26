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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/annotations"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/class"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/compile"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	sessionsFixture = "sessions"
	shopRoute       = "HTTPRoute/shop/shop"
	laterPolicy     = "XBackendTrafficPolicy/shop/later"
)

func endpointRouting(o *kubecfg.Options) { o.Defaults.RoutingMode = kubecfg.RoutingModeEndpoint }

// sessionsOf returns the session of each rule of each route the source became, by rule
func sessionsOf(m *ir.IR, src string) [][]*ir.Session {
	var out [][]*ir.Session
	for _, r := range m.Routes {
		if r.Source.Key() != src {
			continue
		}
		var rules []*ir.Session
		for _, rule := range r.Rules {
			rules = append(rules, rule.Session)
		}
		out = append(out, rules)
	}
	return out
}

// memberSession returns the session of the first member of the named source's groups that has one
func memberSession(m *ir.IR, src string) *ir.Session {
	for _, g := range m.Backends {
		for _, mem := range g.Members {
			if g.Source.Key() == src && mem.Session != nil {
				return mem.Session
			}
		}
	}
	return nil
}

func TestTranslateSessionPersistence(t *testing.T) {
	problems := golden(t, sessionsFixture, endpointRouting)
	containing(t, problems, shopRoute, "rule 3", "cookieConfig requires type Cookie; sessions are not kept")
	containing(t, problems, "HTTPRoute/shop/younger",
		`session cookie "shop" is already set for this host by HTTPRoute/shop/shop rule 0`)
	containing(t, problems, laterPolicy,
		"targetRef shop/carts is already selected by the older policy XBackendTrafficPolicy/shop/carts")
	containing(t, problems, laterPolicy, "targetRef kind apps/Deployment is not supported")
	containing(t, problems, laterPolicy, "retryConstraint is not supported and is ignored")
	require.Len(t, problems, 5, "%v", details(problems))

	model, _, _ := translateFixture(t, sessionsFixture, endpointRouting)
	permanent := &ir.Session{Type: ir.SessionCookie, Name: "shop", AbsoluteMS: 3600000, Permanent: true}
	header := &ir.Session{Type: ir.SessionHeader, Name: "X-Session"}
	// one route per host, each keeping the rule's session; the traffic policy's Service keeps its
	// own, and the rule that could not keep one is served without it
	shop := sessionsOf(model, shopRoute)
	require.Len(t, shop, 2)
	for _, rules := range shop {
		require.Equal(t, []*ir.Session{permanent, header, nil, nil}, rules)
	}
	require.Equal(t, &ir.Session{Type: ir.SessionCookie}, memberSession(model, shopRoute))
	require.Equal(t, [][]*ir.Session{{nil}}, sessionsOf(model, "HTTPRoute/shop/younger"))
	require.Equal(t, [][]*ir.Session{{{Type: ir.SessionCookie, Name: "shop"}}},
		sessionsOf(model, "HTTPRoute/shop/elsewhere"))
	require.Equal(t, [][]*ir.Session{{{Type: ir.SessionHeader}}}, sessionsOf(model, "GRPCRoute/shop/rpc"))
}

func TestTranslateSessionsInServiceRouting(t *testing.T) {
	// kube-proxy picks the endpoint of a Service reached by its cluster IP, so only a rule over
	// several Services has an ALB to keep a session, and it keeps the client on its Service alone
	model, problems, _ := translateFixture(t, sessionsFixture)
	containing(t, problems, shopRoute, "rule 0",
		"keeps a session on its Service, and kube-proxy chooses the endpoint")
	containing(t, problems, shopRoute, "rule 1", "needs the endpoint routing mode; sessions are not kept")
	containing(t, problems, "GRPCRoute/shop/rpc", "needs the endpoint routing mode")
	shop := sessionsOf(model, shopRoute)
	require.NotNil(t, shop[0][0])
	require.Nil(t, shop[0][1])
	require.Nil(t, memberSession(model, shopRoute), "a Service's own session needs its endpoints")
}

func TestLowerSession(t *testing.T) {
	cookie, header := gwapiv1.CookieBasedSessionPersistence, gwapiv1.HeaderBasedSessionPersistence
	permanent, session := gwapiv1.PermanentCookieLifetimeType, gwapiv1.SessionCookieLifetimeType
	name := func(s string) *string { return &s }
	d := func(s string) *gwapiv1.Duration { v := gwapiv1.Duration(s); return &v }
	for label, test := range map[string]struct {
		in   gwapiv1.SessionPersistence
		want *ir.Session
		err  string
	}{
		"defaults":         {gwapiv1.SessionPersistence{}, &ir.Session{Type: ir.SessionCookie}, ""},
		"sub-second":       {gwapiv1.SessionPersistence{AbsoluteTimeout: d("10ms")}, &ir.Session{Type: ir.SessionCookie, AbsoluteMS: 1000}, ""},
		"session lifetime": {gwapiv1.SessionPersistence{Type: &cookie, CookieConfig: &gwapiv1.CookieConfig{LifetimeType: &session}}, &ir.Session{Type: ir.SessionCookie}, ""},
		"secure prefix":    {gwapiv1.SessionPersistence{SessionName: name("__Host-s")}, &ir.Session{Type: ir.SessionCookie, Name: "__Host-s"}, ""},
		"header":           {gwapiv1.SessionPersistence{Type: &header, SessionName: name("X-S")}, &ir.Session{Type: ir.SessionHeader, Name: "X-S"}, ""},
		"bad type":         {gwapiv1.SessionPersistence{Type: new(gwapiv1.SessionPersistenceType("Query"))}, nil, "must be Cookie or Header"},
		"bad cookie name":  {gwapiv1.SessionPersistence{SessionName: name("a b")}, nil, `sessionName "a b"`},
		"bad header name":  {gwapiv1.SessionPersistence{Type: &header, SessionName: name("X S")}, nil, `sessionName "X S"`},
		"bad timeout":      {gwapiv1.SessionPersistence{AbsoluteTimeout: d("soon")}, nil, "absoluteTimeout"},
		"permanent, no timeout": {
			gwapiv1.SessionPersistence{CookieConfig: &gwapiv1.CookieConfig{LifetimeType: &permanent}},
			nil,
			"requires sessionPersistence.absoluteTimeout",
		},
		"bad lifetime": {gwapiv1.SessionPersistence{CookieConfig: &gwapiv1.CookieConfig{
			LifetimeType: new(gwapiv1.CookieLifetimeType("Forever")),
		}}, nil, "must be Session or Permanent"},
	} {
		got, err := lowerSession(&test.in)
		if test.err != "" {
			require.ErrorContains(t, err, test.err, label)
			continue
		}
		require.NoError(t, err, label)
		require.Equal(t, test.want, got, label)
	}
	got, err := lowerSession(nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestTranslateServiceSessions(t *testing.T) {
	// a Service's session gives way to its rule's own, and a Service's named cookie is subject to
	// the same one-owner-per-host rule as a rule's
	c := load(t, filepath.Join("testdata", sessionsFixture+".yaml"))
	carts := "carts"
	c.traffic[0].Spec.SessionPersistence.SessionName = &carts
	c.traffic[1].Spec.SessionPersistence.Type = new(gwapiv1.SessionPersistenceType("Query"))
	c.routes[0].Spec.Rules[1].BackendRefs[0].Name = gwapiv1.ObjectName(carts)
	c.routes[1].Spec.Rules[0].BackendRefs[0].Name = gwapiv1.ObjectName(carts)
	model, _, problems := Translate(Config{
		Cache: c, Claimer: class.New(controllerName, ""), Options: options(t, endpointRouting),
	})
	containing(t, problems, laterPolicy, "sessionPersistence.type must be Cookie or Header")
	containing(t, problems, "HTTPRoute/shop/younger", "backendRef 0",
		`session cookie "carts" is already set for this host by HTTPRoute/shop/shop backendRef 0`)
	var kept int
	for _, g := range model.Backends {
		for _, m := range g.Members {
			if m.Session == nil {
				continue
			}
			kept++
			require.Equal(t, shopRoute, g.Source.Key())
			require.Equal(t, 2, g.RuleIndex%4, "only the rule with no session of its own keeps carts'")
		}
	}
	require.Equal(t, 2, kept, "one per host")
}

func TestTranslateSessionCookiesSpanListeners(t *testing.T) {
	// a browser sends a host's cookies to every port and scheme, so a route on another listener,
	// plain or TLS, cannot set a cookie an older route sets for the same host
	for _, listener := range []gwapiv1.Listener{
		{Name: "alt", Port: 8080, Protocol: gwapiv1.HTTPProtocolType},
		{Name: "secure", Port: 443, Protocol: gwapiv1.HTTPSProtocolType, TLS: &gwapiv1.ListenerTLSConfig{
			CertificateRefs: []gwapiv1.SecretObjectReference{{Name: "shop-tls"}},
		}},
	} {
		c := load(t, filepath.Join("testdata", sessionsFixture+".yaml"))
		c.gateways[0].Spec.Listeners = append(c.gateways[0].Spec.Listeners, listener)
		section := listener.Name
		for _, hr := range c.routes[1:] {
			hr.Spec.ParentRefs[0].SectionName = &section
		}
		_, _, problems := Translate(Config{
			Cache: c, Claimer: class.New(controllerName, ""), Options: options(t, endpointRouting),
		})
		containing(t, problems, "HTTPRoute/shop/younger",
			`session cookie "shop" is already set for this host by HTTPRoute/shop/shop rule 0`)
		for _, p := range problems {
			require.NotContains(t, p.String(), "HTTPRoute/shop/elsewhere", "another host keeps the name")
		}
	}
}

// classKeyName is the Secret the class-params fixture's sticky_secret names
const classKeyName = "sticky-key"

func keySecret(labeled bool, data map[string][]byte) *corev1.Secret {
	s := &corev1.Secret{
		Namespace: "infra", Name: classKeyName,
		Type: corev1.SecretTypeOpaque, Data: data,
	}
	if labeled {
		s.Labels = map[string]string{annotations.LabelStickyKey: ""}
	}
	return s
}

// classKey returns the key the class's policy carries into the model, and the model's hash
func classKey(t *testing.T, secret *corev1.Secret) (string, string, []Problem) {
	t.Helper()
	model, problems := classWith(t, func(c *cache) {
		c.configMaps["infra/gateway-params"].Data[ParamStickySecret] = classKeyName
		if secret != nil {
			c.secrets["infra/"+classKeyName] = secret
		}
	})
	require.NotEmpty(t, model.Routes, "a class whose key cannot be read is still served")
	for _, p := range model.Policies {
		if p.Source.Kind == ir.KindGatewayClass {
			return p.StickySecret, model.Hash(), problems
		}
	}
	t.Fatal("the class produced no policy")
	return "", "", nil
}

func TestTranslateClassStickySecret(t *testing.T) {
	first := []byte(strings.Repeat("a", 32))
	key, before, problems := classKey(t, keySecret(true, map[string][]byte{stickySecretKey: first}))
	require.Empty(t, problems)
	require.Equal(t, base64.StdEncoding.EncodeToString(first), key)

	// a rotated key changes the model, so the new key reaches every ALB the class generates
	second := []byte(strings.Repeat("b", 40))
	key, after, _ := classKey(t, keySecret(true, map[string][]byte{stickySecretKey: second}))
	require.Equal(t, base64.StdEncoding.EncodeToString(second), key)
	require.NotEqual(t, before, after)

	// a key that cannot be read is reported, and the class keys its tokens as if it named none
	for detail, secret := range map[string]*corev1.Secret{
		"is not found, or is not labeled": nil,
		"is not labeled " + annotations.LabelStickyKey: keySecret(false,
			map[string][]byte{stickySecretKey: first}),
		`has no "key" entry`: keySecret(true, map[string][]byte{"token": first}),
		"holds a 4-byte key": keySecret(true, map[string][]byte{stickySecretKey: []byte("tiny")}),
	} {
		key, _, problems := classKey(t, secret)
		require.Empty(t, key, detail)
		containing(t, problems, "GatewayClass//trickster", `parameter "sticky_secret"`, detail,
			"session tokens are keyed by kubernetes.defaults.sticky_secret_file, else per process")
		require.Equal(t, ir.ReasonInvalidParameters, problems[0].Reason)
	}

	// a Secret in another namespace cannot be named, which refuses the class as any bad parameter
	model, problems := classWith(t, func(c *cache) {
		c.configMaps["infra/gateway-params"].Data[ParamStickySecret] = "shop/" + classKeyName
	})
	require.Empty(t, model.Routes)
	containing(t, problems, `parameter "sticky_secret" rejected`, "parameters ConfigMap's namespace")
}

// loadsInEndpointMode translates the cache in the endpoint routing mode, compiles it, and loads
// and validates the result as the daemon's reload would
func loadsInEndpointMode(t *testing.T, c *cache) ([]Problem, error) {
	t.Helper()
	o := options(t, endpointRouting)
	model, _, problems := Translate(Config{Cache: c, Claimer: class.New(controllerName, ""), Options: o})
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(baseConfig), 0o600))
	conf, err := config.LoadWithOverlay([]string{"-config", path}, overlay)
	require.NoError(t, err)
	require.NoError(t, conf.Backends.Validate())
	if err := validate.Validate(conf); err != nil {
		return problems, err
	}
	require.NoError(t, conf.Process())
	return problems, validate.RoutesRulesAndPools(conf, make(backends.Backends, len(conf.Backends)))
}

func TestSessionsLoadInEndpointRouting(t *testing.T) {
	// the fixture, its GRPCRoute included, loads in the mode its golden was made in
	_, err := loadsInEndpointMode(t, load(t, filepath.Join("testdata", sessionsFixture+".yaml")))
	require.NoError(t, err)

	// a Service's named cookie reached through weighted routes on disjoint hosts is set by ALBs
	// with no hosts of their own, which validation reads from the routes that reach them
	c := load(t, filepath.Join("testdata", sessionsFixture+".yaml"))
	carts := "carts"
	c.traffic[0].Spec.SessionPersistence.SessionName = &carts
	for _, hr := range c.routes[1:] {
		// younger (b.example.com) and elsewhere (c.example.com), each a Service pair
		hr.Spec.Rules[0].BackendRefs[1].Name = gwapiv1.ObjectName(carts)
		hr.Spec.Rules[0].SessionPersistence = nil
	}
	c.routes[1].Spec.Hostnames = []gwapiv1.Hostname{"d.example.com"}
	problems, err := loadsInEndpointMode(t, c)
	require.NoError(t, err)
	for _, p := range problems {
		require.NotContains(t, p.String(), "already set", "the hosts are disjoint")
	}
}
