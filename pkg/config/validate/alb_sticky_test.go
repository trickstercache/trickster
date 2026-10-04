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
package validate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ur "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	sticky "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
)

func TestStickyWarnsOfAPerProcessKeyOnHTTP(t *testing.T) {
	stickyALB := func(o *sticky.Options, listeners ...string) *config.Config {
		if err := o.Initialize(); err != nil {
			t.Fatal(err)
		}
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTCP
		b := bo.New()
		b.Provider = providers.ALB
		b.ListenerNames = listeners
		b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: o}
		c.Backends = bo.Lookup{"lb": b}
		return c
	}
	warned := func(c *config.Config) bool {
		for _, w := range c.LoaderWarnings {
			if strings.Contains(w, "sticky.secret") && strings.Contains(w, `alb "lb"`) {
				return true
			}
		}
		return false
	}
	key := sticky.Options{Secret: "0123456789abcdef0123456789abcdef"}
	for name, test := range map[string]struct {
		o         sticky.Options
		listeners []string
		streams   []string
		want      bool
	}{
		"default mode on http":   {sticky.Options{}, nil, nil, true},
		"cookie on http":         {sticky.Options{Mode: sticky.ModeCookie}, nil, nil, true},
		"header on http":         {sticky.Options{Mode: sticky.ModeHeader}, nil, nil, true},
		"default mode on both":   {sticky.Options{}, []string{"relay", "default"}, []string{"lb"}, true},
		"table on http":          {sticky.Options{Mode: sticky.ModeTable}, nil, nil, false},
		"a configured key":       {key, nil, nil, false},
		"default mode on stream": {sticky.Options{}, []string{"relay"}, []string{"lb"}, false},
	} {
		o := test.o
		c := stickyALB(&o, test.listeners...)
		if err := requestALBs(c, sets.New(test.streams)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := warned(c); got != test.want {
			t.Errorf("%s: warned = %v, want %v (%v)", name, got, test.want, c.LoaderWarnings)
		}
	}
}

// initializedSticky parses a sticky block as config loading would
func initializedSticky(t *testing.T, o sticky.Options) *sticky.Options {
	t.Helper()
	if err := o.Initialize(); err != nil {
		t.Fatal(err)
	}
	return &o
}

// an ALB's sticky block must suit every listener it serves: tokens need an http listener, and a
// table's key must be one each listener can read
func TestStickySuitsEveryListener(t *testing.T) {
	web := func(c *config.Config) {
		c.Listeners["web"] = listener.New("web")
		c.Listeners["web"].ListenPort = 18480
	}
	relay := func(c *config.Config) {
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol = listener.ProtocolTLS
		c.Listeners["relay"].ListenPort = 9443
	}
	for name, test := range map[string]struct {
		o         sticky.Options
		listeners []string
		want      string
	}{
		"default mode on both":    {sticky.Options{}, []string{"relay", "web"}, ""},
		"sni table on tls":        {sticky.Options{Table: sticky.TableOptions{Key: "sni"}}, []string{"relay"}, ""},
		"header table on http":    {sticky.Options{Mode: sticky.ModeTable, Table: sticky.TableOptions{Key: "header:X-Client"}}, []string{"web"}, ""},
		"cookie on a tls relay":   {sticky.Options{Mode: sticky.ModeCookie}, []string{"relay", "web"}, "cannot carry the tokens of alb backend \"lb\"'s sticky.mode \"cookie\""},
		"header with no http":     {sticky.Options{Mode: sticky.ModeHeader}, []string{"relay"}, "cannot carry the tokens"},
		"header table on tls":     {sticky.Options{Table: sticky.TableOptions{Key: "header:X-Client"}}, []string{"relay"}, "cannot read alb backend \"lb\"'s sticky.table.key \"header:X-Client\""},
		"sni table on http":       {sticky.Options{Mode: sticky.ModeTable, Table: sticky.TableOptions{Key: "sni"}}, []string{"relay", "web"}, "sticky.table.key \"sni\" cannot be read from a request, which http listener \"web\" serves"},
		"sni table, http default": {sticky.Options{Table: sticky.TableOptions{Key: "sni"}}, []string{"relay", "web"}, ""},
	} {
		c := config.NewConfig()
		web(c)
		relay(c)
		lb := bo.New()
		lb.Provider = providers.ALB
		lb.ListenerNames = test.listeners
		lb.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.Members("m1"), Sticky: initializedSticky(t, test.o)}
		member := bo.New()
		member.Provider = providers.ReverseProxyShort
		member.OriginURL = "tcp://member.example.com:9000"
		c.Backends = bo.Lookup{"lb": lb, "m1": member}
		err := Listeners(c)
		if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
	// an ALB whose only listener is a stream one cannot issue tokens, whatever else it serves
	c := config.NewConfig()
	relay(c)
	lb := bo.New()
	lb.Provider = providers.ALB
	lb.ListenerNames = []string{"relay"}
	lb.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, sticky.Options{Mode: sticky.ModeCookie})}
	c.Backends = bo.Lookup{"lb": lb}
	if err := requestALBs(c, sets.New([]string{"lb"})); err == nil ||
		!strings.Contains(err.Error(), "only an http listener carries, and it serves none") {
		t.Errorf("error = %v", err)
	}
}

func TestStickyOnNativeListeners(t *testing.T) {
	for name, test := range map[string]struct {
		o    sticky.Options
		want string
	}{
		"user table":   {sticky.Options{Table: sticky.TableOptions{Key: "user"}}, ""},
		"default":      {sticky.Options{}, ""},
		"host table":   {sticky.Options{Table: sticky.TableOptions{Key: "host"}}, "cannot read alb backend \"replicas\"'s sticky.table.key \"host\""},
		"cookie token": {sticky.Options{Mode: sticky.ModeCookie}, "cannot carry the tokens"},
	} {
		c := replicaConfig("rr")
		c.Backends["replicas"].ALBOptions.Sticky = initializedSticky(t, test.o)
		err := Listeners(c)
		if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
}

// two ALBs that set one cookie for one host would each replace the other's token, whatever ports
func TestStickyCookiesAreDistinctPerHost(t *testing.T) {
	const (
		hostA   = "a.example.com"
		hostB   = "b.example.com"
		hostAny = "**.example.com"
	)
	// an ALB naming hosts registers only them unless pathRouting also answers /name/... everywhere
	type alb struct {
		o           sticky.Options
		listeners   []string
		hosts       []string
		anyHost     bool
		pathRouting bool
	}
	build := func(first, second alb) *config.Config {
		c := config.NewConfig()
		c.Listeners["web2"] = listener.New("web2")
		c.Listeners["web2"].ListenPort = 18481
		c.Listeners["tcp"] = listener.New("tcp")
		c.Listeners["tcp"].Protocol = listener.ProtocolTCP
		c.Backends = bo.Lookup{}
		for name, a := range map[string]alb{"first": first, "second": second} {
			b := bo.New()
			b.Provider = providers.ALB
			b.ListenerNames = a.listeners
			b.Hosts = a.hosts
			b.AnyHostRouting = a.anyHost
			b.PathRoutingDisabled = len(a.hosts) > 0 && !a.pathRouting
			b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, a.o)}
			c.Backends[name] = b
		}
		return c
	}
	domain := sticky.Options{Cookie: sticky.CookieOptions{Domain: "example.com"}}
	for name, test := range map[string]struct {
		first, second alb
		want          string
	}{
		"same cookie":     {alb{}, alb{}, `alb backends "first" and "second" both set sticky cookie "trickster_sticky" for a host they both serve`},
		"own name":        {alb{}, alb{o: sticky.Options{Cookie: sticky.CookieOptions{Name: "second"}}}, ""},
		"own path":        {alb{}, alb{o: sticky.Options{Cookie: sticky.CookieOptions{Path: "/second"}}}, ""},
		"other listener":  {alb{}, alb{listeners: []string{"web2"}}, "for a host they both serve"},
		"other port host": {alb{hosts: []string{hostA}}, alb{listeners: []string{"web2"}, hosts: []string{hostB}}, ""},
		"stream only":     {alb{}, alb{listeners: []string{"tcp"}}, ""},
		"header mode":     {alb{}, alb{o: sticky.Options{Mode: sticky.ModeHeader}}, ""},
		"table mode":      {alb{}, alb{o: sticky.Options{Mode: sticky.ModeTable}}, ""},
		"other hosts":     {alb{hosts: []string{hostA}}, alb{hosts: []string{hostB}}, ""},
		"shared host":     {alb{hosts: []string{hostA}}, alb{hosts: []string{hostB, hostA}}, "for a host they both serve"},
		"wildcard host":   {alb{hosts: []string{hostA}}, alb{hosts: []string{hostAny}}, "for a host they both serve"},
		"any host":        {alb{hosts: []string{hostA}}, alb{hosts: []string{hostB}, anyHost: true}, "for a host they both serve"},
		"no hosts":        {alb{hosts: []string{hostA}}, alb{}, "for a host they both serve"},
		"domain cookie":   {alb{o: domain, hosts: []string{hostA}}, alb{o: domain, hosts: []string{hostB}}, "for a host they both serve"},
		// /second/... is registered on every host, so its cookie reaches a.example.com too
		"path routing": {alb{hosts: []string{hostA}}, alb{hosts: []string{hostB}, pathRouting: true}, "for a host they both serve"},
	} {
		err := stickyCookies(build(test.first, test.second), everyVisible)
		if (test.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: error = %v, want %q", name, err, test.want)
		}
	}
}

// a backend registering no route of its own is served for the hosts of whatever dispatches into
// it, so two such ALBs setting one cookie conflict only when their dispatchers share a host
func TestStickyCookiesFollowDispatchers(t *testing.T) {
	const (
		hostA = "a.example.com"
		hostB = "b.example.com"
		inner = "inner"
		twin  = "twin"
	)
	// dispatchOnly is an ALB registering no route of its own, setting the default cookie
	dispatchOnly := func() *bo.Options {
		b := bo.New()
		b.Provider = providers.ALB
		b.PathRoutingDisabled = true
		b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, sticky.Options{})}
		return b
	}
	// front dispatches into the named backend on the given hosts, however kind says
	front := func(kind, child string, hosts ...string) *bo.Options {
		b := bo.New()
		b.Hosts = hosts
		b.PathRoutingDisabled = true
		switch kind {
		case "alb":
			b.Provider = providers.ALB
			b.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: child}}}
		case "rule":
			b.Provider = providers.Rule
			b.RuleName = "r-" + child
		case "case":
			b.Provider = providers.Rule
			b.RuleName = "c-" + child
		case "user":
			b.Provider = providers.ALB
			b.ALBOptions = &ao.Options{MechanismName: "ur", UserRouter: &ur.Options{
				Users: ur.UserMappingOptionsByUser{"alice": nil, "bob": {ToBackend: child}},
			}}
		case "default":
			b.Provider = providers.ALB
			b.ALBOptions = &ao.Options{MechanismName: "ur", UserRouter: &ur.Options{DefaultBackend: child}}
		case "mirror":
			b.Provider = providers.ReverseProxyShort
			b.Paths = po.List{nil, {Path: "/", Mirrors: []*po.MirrorOptions{{BackendName: child}}}}
		}
		return b
	}
	build := func(backends map[string]*bo.Options) *config.Config {
		c := config.NewConfig()
		c.Backends = bo.Lookup(backends)
		c.Rules = rule.Lookup{
			"r-" + inner: {NextRoute: inner}, "r-" + twin: {NextRoute: twin},
			"c-" + inner: {CaseOptions: rule.CaseOptionsList{{NextRoute: inner}}},
			"c-" + twin:  {CaseOptions: rule.CaseOptionsList{nil, {NextRoute: twin}}},
		}
		return c
	}
	for _, kind := range []string{"alb", "rule", "case", "user", "default"} {
		disjoint := build(map[string]*bo.Options{
			inner: dispatchOnly(), twin: dispatchOnly(),
			"fa": front(kind, inner, hostA), "fb": front(kind, twin, hostB),
		})
		if err := stickyCookies(disjoint, everyVisible); err != nil {
			t.Errorf("%s dispatchers on disjoint hosts: %v", kind, err)
		}
		shared := build(map[string]*bo.Options{
			inner: dispatchOnly(), twin: dispatchOnly(),
			"fa": front(kind, inner, hostA), "fb": front(kind, twin, hostB, hostA),
		})
		if err := stickyCookies(shared, everyVisible); err == nil {
			t.Errorf("%s dispatchers sharing a host: no error", kind)
		}
	}
	// a backend registered on its own hosts also serves every host a dispatcher reaches it on, so
	// a cookie it sets there meets another ALB's on that host
	for _, kind := range []string{"alb", "rule", "case", "user", "default"} {
		direct := func(dispatcherHost string) *config.Config {
			m := map[string]*bo.Options{
				inner: dispatchOnly(), twin: dispatchOnly(),
				"fa": front(kind, inner, dispatcherHost), "fb": front("alb", twin, hostB),
			}
			m[inner].Hosts = []string{hostA}
			return build(m)
		}
		if err := stickyCookies(direct(hostB), everyVisible); err == nil {
			t.Errorf("%s dispatcher on the other ALB's host: no error", kind)
		}
		if err := stickyCookies(direct(hostA), everyVisible); err != nil {
			t.Errorf("%s dispatcher on the ALB's own host: %v", kind, err)
		}
	}
	// a mirror discards its target's response, cookie and all, so mirroring from a host does not
	// make the target set its cookie there
	mirrored := build(map[string]*bo.Options{
		inner: dispatchOnly(), twin: dispatchOnly(),
		"fa": front("mirror", inner, hostA), "fb": front("alb", twin, hostA),
	})
	mirrored.Backends[inner].Hosts = []string{hostB}
	if err := stickyCookies(mirrored, everyVisible); err != nil {
		t.Errorf("a mirror from the other ALB's host: %v", err)
	}
	for name, mutate := range map[string]func(map[string]*bo.Options){
		// one of twin's dispatchers serves every host
		"any-host dispatcher": func(m map[string]*bo.Options) {
			m["fc"] = front("alb", twin)
			m["fc"].AnyHostRouting = true
		},
		// reached under its own name on every host
		"path routing": func(m map[string]*bo.Options) { m[twin].PathRoutingDisabled = false },
		// the default backend answers what no other route claims, on every host
		"marked default": func(m map[string]*bo.Options) { m[twin].IsDefault = true },
		"named default": func(m map[string]*bo.Options) {
			m["default"] = m[twin]
			delete(m, twin)
			m["fb"].ALBOptions.Pool = ao.PoolMemberList{{Name: "default"}}
		},
		// a dispatch cycle ends, and its members serve what reaches the cycle from outside
		"cycle reached on the shared host": func(m map[string]*bo.Options) {
			m["fb"] = front("alb", twin)
			m["fb"].Hosts = nil
			m[twin].ALBOptions.Pool = ao.PoolMemberList{{Name: "fb"}}
			m["fc"] = front("alb", "fb", hostA)
		},
	} {
		m := map[string]*bo.Options{
			inner: dispatchOnly(), twin: dispatchOnly(),
			"fa": front("alb", inner, hostA), "fb": front("alb", twin, hostB),
		}
		mutate(m)
		if err := stickyCookies(build(m), everyVisible); err == nil {
			t.Errorf("%s: no error, though twin is served on a host inner serves", name)
		}
	}
}

func TestServedHostsLeavesOwnHostsAlone(t *testing.T) {
	// joining a dispatcher's hosts to a backend's own must not write into the backend's own list
	own := make([]string, 1, 4)
	own[0] = "a.example.com"
	child, parent := bo.New(), bo.New()
	child.Hosts = own
	parent.Hosts = []string{"b.example.com"}
	child.PathRoutingDisabled, parent.PathRoutingDisabled = true, true
	parent.Provider = providers.ALB
	parent.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: "child"}}}
	c := config.NewConfig()
	c.Backends = bo.Lookup{"child": child, "parent": parent}
	got, every := servedHosts(c, everyVisible)("child")
	if every {
		t.Error("the child serves two hosts, not every host")
	}
	if len(got) != 2 || got[0] != "a.example.com" || got[1] != "b.example.com" {
		t.Errorf("served hosts = %v", got)
	}
	if len(child.Hosts) != 1 || own[:2][1] != "" {
		t.Errorf("the backend's own hosts were written into: %v", own[:2])
	}
}

// a backend no client reaches sets no cookie a browser holds, however it is mirrored or looped
func TestStickyCookiesSkipUnreachedALBs(t *testing.T) {
	stickyALB := func(hosts ...string) *bo.Options {
		b := bo.New()
		b.Provider = providers.ALB
		b.Hosts = hosts
		b.PathRoutingDisabled = len(hosts) == 0
		b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, sticky.Options{})}
		return b
	}
	mirror := bo.New()
	mirror.Provider = providers.ReverseProxyShort
	mirror.AnyHostRouting = true
	mirror.Paths = po.List{{Path: "/", Mirrors: []*po.MirrorOptions{{BackendName: "mirrored"}}}}
	direct := stickyALB()
	direct.PathRoutingDisabled = false
	for name, backends := range map[string]bo.Lookup{
		// the defect's case: a mirror's target beside a sticky ALB on every host
		"mirror only":  {"mirrored": stickyALB(), "front": mirror, "direct": direct},
		"unreferenced": {"orphan": stickyALB(), "direct": direct},
		// a loop no client enters
		"closed cycle": {
			"loop-a": withPool(stickyALB(), "loop-b"), "loop-b": withPool(stickyALB(), "loop-a"),
			"direct": direct,
		},
	} {
		c := config.NewConfig()
		c.Backends = backends
		if err := stickyCookies(c, everyVisible); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func withPool(b *bo.Options, members ...string) *bo.Options {
	for _, m := range members {
		b.ALBOptions.Pool = append(b.ALBOptions.Pool, ao.PoolMember{Name: m})
	}
	return b
}

// everyVisible has every backend register a path on a listener, as one with any ordinary path does
func everyVisible(string) bool { return true }

func TestStickyCookiesFollowListenerVisiblePaths(t *testing.T) {
	// an ALB whose paths all stay off the listeners is reached only through its dispatchers, so
	// path routing on it answers no host of its own
	const hostA, hostB = "a.example.com", "b.example.com"
	build := func() *config.Config {
		c := config.NewConfig()
		c.Backends = bo.Lookup{}
		for child, host := range map[string]string{"inner": hostA, "twin": hostB} {
			b := bo.New()
			b.Provider = providers.ALB
			b.ALBOptions = &ao.Options{MechanismName: "rr", Sticky: initializedSticky(t, sticky.Options{})}
			c.Backends[child] = b
			parent := bo.New()
			parent.Provider = providers.ALB
			parent.Hosts = []string{host}
			parent.PathRoutingDisabled = true
			parent.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: child}}}
			c.Backends["front-"+child] = parent
		}
		return c
	}
	dispatchOnly := func(name string) bool { return name != "inner" && name != "twin" }
	if err := stickyCookies(build(), dispatchOnly); err != nil {
		t.Errorf("dispatch-only ALBs behind disjoint hosts: %v", err)
	}
	if err := stickyCookies(build(), everyVisible); err == nil {
		t.Error("path-routed ALBs: no error, though each answers /name/... on every host")
	}
}

// stickyPipelineConfig is two sticky ALBs sharing the default cookie, each in the pool of an ALB
// on its own host; childPaths is the YAML each child's path settings are replaced with
const stickyPipelineConfig = `
backends:
  origin:
    provider: rp
    origin_url: http://127.0.0.1:1
  front-a:
    provider: alb
    hosts: [a.example.com]
    path_routing_disabled: true
    alb: {mechanism: rr, pool: [{name: inner}]}
  front-b:
    provider: alb
    hosts: [b.example.com]
    path_routing_disabled: true
    alb: {mechanism: rr, pool: [{name: twin}]}
  inner:
    provider: alb
    alb: {mechanism: rr, pool: [{name: origin}], sticky: {}}
CHILD_PATHS
  twin:
    provider: alb
    alb: {mechanism: rr, pool: [{name: origin}], sticky: {}}
CHILD_PATHS
`

func TestStickyCookiesThroughTheRouter(t *testing.T) {
	// the router decides what reaches a listener, so validation reads each ALB's paths as it will
	// register them: its own laid over its provider's defaults
	for name, test := range map[string]struct {
		childPaths string
		want       string
	}{
		"dispatch only": {"    path_defaults_disabled: true\n" +
			"    paths: [{path: /, match_type: prefix, handler: alb, methods: ['*'], dispatch_only: true}]", ""},
		"listener path": {"    path_defaults_disabled: true\n" +
			"    paths: [{path: /, match_type: prefix, handler: alb, methods: ['*']}]", "for a host they both serve"},
		// the provider's default path still registers the methods a dispatch-only path leaves
		"defaults remain": {
			"    paths: [{path: /, match_type: prefix, handler: alb, methods: [GET], dispatch_only: true}]",
			"for a host they both serve",
		},
		"provider defaults": {"", "for a host they both serve"},
		// a path naming a handler the ALB does not have is skipped by the router
		"unknown handler": {"    path_defaults_disabled: true\n" +
			"    paths: [{path: /, match_type: prefix, handler: proxycache, methods: ['*']}]", ""},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trickster.yaml")
			doc := strings.ReplaceAll(stickyPipelineConfig, "CHILD_PATHS\n", test.childPaths+"\n")
			require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
			c, err := config.Load([]string{"-config", path})
			require.NoError(t, err)
			require.NoError(t, Validate(c))
			require.NoError(t, c.Process())
			clients := make(backends.Backends, len(c.Backends))
			err = RoutesRulesAndPools(c, clients)
			if name == "provider defaults" {
				registersOnListenerEdges(t, clients["inner"])
				// a backend is judged once, however many ALBs it dispatches for
				visible := listenerVisible(c, clients)
				require.True(t, visible("inner"))
				c.Backends["inner"].PathDefaultsDisabled = true
				require.True(t, visible("inner"), "the first judgment stands")
			}
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}

// registersOnListenerEdges covers what a configuration cannot express: a path with no methods, and
// a backend or client that is missing, which is taken to register
func registersOnListenerEdges(t *testing.T, client backends.Backend) {
	t.Helper()
	require.NotNil(t, client)
	noMethods := &bo.Options{PathDefaultsDisabled: true, Paths: po.List{nil, {Path: "/", HandlerName: "alb"}}}
	require.False(t, registersOnListener(noMethods, client))
	require.True(t, registersOnListener(nil, client))
	require.True(t, registersOnListener(noMethods, nil))
}

// diamond is depth layers of two ALBs, each dispatching to both in the next layer, with the top
// layer on a.example.com and b.example.com and the bottom layer setting one sticky cookie; a
// bottom ALB is reached along 2^(depth-1) paths
func diamond(t *testing.T, depth int) *config.Config {
	t.Helper()
	c := config.NewConfig()
	c.Backends = bo.Lookup{}
	for layer := range depth {
		for _, side := range []string{"a", "b"} {
			b := bo.New()
			b.Provider = providers.ALB
			b.PathRoutingDisabled = true
			b.ALBOptions = &ao.Options{MechanismName: "rr"}
			if layer == 0 {
				b.Hosts = []string{side + ".example.com"}
			}
			if layer == depth-1 {
				b.ALBOptions.Sticky = initializedSticky(t, sticky.Options{})
			} else {
				b.ALBOptions.Pool = ao.PoolMemberList{{Name: diamondNode(layer+1, "a")}, {Name: diamondNode(layer+1, "b")}}
			}
			c.Backends[diamondNode(layer, side)] = b
		}
	}
	return c
}

func diamondNode(layer int, side string) string {
	return "l" + strconv.Itoa(layer) + side
}

func TestServedHostsGrowWithTheGraph(t *testing.T) {
	// each backend is judged once per sticky ALB, however many dispatch paths reach it, and each
	// host is kept once
	for _, depth := range []int{8, 16, 40} {
		c := diamond(t, depth)
		var judged int
		visible := func(string) bool {
			judged++
			return true
		}
		hosts, every := servedHosts(c, visible)(diamondNode(depth-1, "a"))
		require.False(t, every, "depth %d", depth)
		require.ElementsMatch(t, []string{"a.example.com", "b.example.com"}, hosts, "depth %d", depth)
		// its sibling in the bottom layer is no dispatcher of it, so is not visited
		require.Equal(t, 2*depth-1, judged, "depth %d: every dispatcher judged once", depth)

		judged = 0
		require.ErrorContains(t, stickyCookies(c, visible), "for a host they both serve", "depth %d", depth)
		require.LessOrEqual(t, judged, 2*2*depth, "depth %d: two sticky ALBs, one walk each", depth)
	}
}

func TestServedHostsKeepEachHostOnce(t *testing.T) {
	// two dispatchers on one host give the backend that host once
	c := diamond(t, 2)
	c.Backends[diamondNode(0, "b")].Hosts = []string{"a.example.com"}
	hosts, every := servedHosts(c, everyVisible)(diamondNode(1, "a"))
	require.False(t, every)
	require.Equal(t, []string{"a.example.com"}, hosts)
}
