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

package pgwire

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	albopt "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	routeTestRouter  = "pg-router"
	routeTestAlpha   = "pg-alpha"
	routeTestBeta    = "pg-beta"
	routeTestOther   = "analyst"
	routeTestUnknown = "stranger"
)

type stubResolver struct {
	routes   map[string]string
	fallback string
	down     map[string]bool
	backends map[string]backends.Backend
}

type downStatus struct{}

func (downStatus) Get() int32 { return -1 }

func (r *stubResolver) ResolveRoute(input backends.RouteInput) (backends.RouteDecision, bool) {
	name, outcome := r.routes[input.Username], backends.RouteOutcomeSelected
	if name == "" {
		name, outcome = r.fallback, backends.RouteOutcomeDefault
	}
	if name == "" {
		return backends.RouteDecision{Outcome: backends.RouteOutcomeNoRoute}, false
	}
	target := backends.RouteTarget{Backend: r.backends[name]}
	if r.down[name] {
		target.Status = downStatus{}
		outcome = backends.RouteOutcomeUnavailable
	}
	return backends.RouteDecision{Target: target, Outcome: outcome}, true
}

func routeTestBackend(t *testing.T, name string) backends.Backend {
	t.Helper()
	o := bo.New()
	o.Name = name
	b, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func routedTestServer(t *testing.T, resolver *stubResolver) (*Server, string, map[string]*fakeUpstream) {
	t.Helper()
	origins := map[string]*fakeUpstream{
		routeTestAlpha: newFakeUpstream(t, cleartextUpstream), routeTestBeta: newFakeUpstream(t, cleartextUpstream),
	}
	targets := make(map[string]Config, len(origins))
	resolver.backends = make(map[string]backends.Backend, len(origins))
	for name, origin := range origins {
		target := terminatedConfig(origin, testClientPass)
		target.BackendName, target.Users = name, nil
		targets[name] = target
		resolver.backends[name] = routeTestBackend(t, name)
	}
	router := Config{BackendName: routeTestRouter, Provider: providers.ALB, Users: map[string]string{
		testClientUser: testClientPass, routeTestOther: testClientPass, routeTestUnknown: testClientPass,
	}}
	router.ApplyListenerOptions(nil)
	router.HandshakeTimeout = fakeTimeout
	server, err := NewRoutedServer(router, resolver, targets)
	if err != nil {
		t.Fatal(err)
	}
	return server, serveTestServer(t, server), origins
}

func routeSelections(backend string, outcome backends.RouteOutcome) float64 {
	return testutil.ToFloat64(metrics.PGWireRouteSelections.WithLabelValues(routeTestRouter, backend, string(outcome)))
}

func TestRoutedServerPinsSessionsToTheirTarget(t *testing.T) {
	resolver := &stubResolver{routes: map[string]string{testClientUser: routeTestAlpha}, fallback: routeTestBeta}
	_, address, origins := routedTestServer(t, resolver)
	selected, defaulted := routeSelections(routeTestAlpha, backends.RouteOutcomeSelected),
		routeSelections(routeTestBeta, backends.RouteOutcomeDefault)
	mapped := mustDial(t, address, testClientUser, testClientPass)
	other := mustDial(t, address, routeTestOther, testClientPass)
	for _, conn := range []*pgconn.PgConn{mapped, other} {
		if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
			t.Fatalf("%d rows, %v", rows, err)
		}
	}
	if got := origins[routeTestAlpha].received(); !slices.Equal(got, []string{fakeQueryOne}) {
		t.Fatalf("the mapped user's statement went elsewhere: %v", got)
	}
	if got := origins[routeTestBeta].received(); !slices.Equal(got, []string{fakeQueryOne}) {
		t.Fatalf("the unmapped user's statement did not reach the default backend: %v", got)
	}
	// each origin is logged in to with its own credentials, never the client's
	if user := origins[routeTestAlpha].lastStartup()[paramUser]; user != testUpstreamUser {
		t.Fatalf("origin login used %q", user)
	}
	if routeSelections(routeTestAlpha, backends.RouteOutcomeSelected)-selected != 1 ||
		routeSelections(routeTestBeta, backends.RouteOutcomeDefault)-defaulted != 1 {
		t.Fatal("route selections were not counted per target and outcome")
	}
	// a cancel arrives at the router and must reach the origin that holds the session
	cancelRunningQuery(t, mapped, origins[routeTestAlpha], legacySecretLen)
}

func TestRoutedServerRejectsClientsWithoutARoute(t *testing.T) {
	resolver := &stubResolver{
		routes: map[string]string{testClientUser: routeTestAlpha}, down: map[string]bool{routeTestAlpha: true},
	}
	server, address, origins := routedTestServer(t, resolver)
	unavailable := routeSelections("", backends.RouteOutcomeUnavailable)
	noRoute := routeSelections("", backends.RouteOutcomeNoRoute)
	for _, user := range []string{testClientUser, routeTestUnknown} {
		if _, err := dial(t, address, user, testClientPass); sqlstate(err) != sqlstateInvalidAuthSpec {
			t.Fatalf("%s: expected a route rejection, got %v", user, err)
		}
	}
	if _, err := dial(t, address, testClientUser, "wrong"); sqlstate(err) != sqlstateInvalidPassword {
		t.Fatalf("a bad password is an authentication failure, got %v", err)
	}
	if len(origins[routeTestAlpha].received())+len(origins[routeTestBeta].received()) != 0 {
		t.Fatal("no origin may be contacted for a client without a route")
	}
	if routeSelections("", backends.RouteOutcomeUnavailable)-unavailable != 1 ||
		routeSelections("", backends.RouteOutcomeNoRoute)-noRoute != 1 {
		t.Fatal("rejections were not counted by outcome")
	}
	// a reload swaps the resolver for new sessions
	server.UpdateRouteResolver(nil)
	server.UpdateRouteResolver(&stubResolver{fallback: routeTestBeta, backends: resolver.backends})
	conn := mustDial(t, address, routeTestUnknown, testClientPass)
	if rows, err := queryRows(t, conn, fakeQueryOne); err != nil || rows != 1 {
		t.Fatalf("%d rows, %v", rows, err)
	}
	// a resolver naming a backend the listener was not built with is no route either
	server.UpdateRouteResolver(&stubResolver{fallback: routeTestRouter, backends: map[string]backends.Backend{
		routeTestRouter: routeTestBackend(t, routeTestRouter),
	}})
	if _, err := dial(t, address, testClientUser, testClientPass); sqlstate(err) != sqlstateInvalidAuthSpec {
		t.Fatalf("expected a route rejection, got %v", err)
	}
}

func TestNewRoutedServerValidation(t *testing.T) {
	upstream := newFakeUpstream(t, nil)
	router := Config{BackendName: routeTestRouter, Users: map[string]string{testClientUser: testClientPass}}
	target := terminatedConfig(upstream, testClientPass)
	targets := map[string]Config{routeTestAlpha: target}
	resolver := &stubResolver{}
	if _, err := NewRoutedServer(router, nil, targets); err == nil {
		t.Fatal("a resolver is required")
	}
	if _, err := NewRoutedServer(router, resolver, nil); err == nil {
		t.Fatal("targets are required")
	}
	if _, err := NewRoutedServer(Config{BackendName: routeTestRouter}, resolver, targets); err == nil {
		t.Fatal("listener-facing users are required")
	}
	anonymous := target
	anonymous.Upstream.User = ""
	if _, err := NewRoutedServer(router, resolver, map[string]Config{routeTestAlpha: anonymous}); err == nil {
		t.Fatal("a target needs origin credentials")
	}
	unaddressed := target
	unaddressed.Upstream.Address = ""
	if _, err := NewRoutedServer(router, resolver, map[string]Config{routeTestAlpha: unaddressed}); err == nil {
		t.Fatal("a target needs an origin address")
	}
	router.Users = map[string]string{testClientUser: "SCRAM-SHA-256$bad"}
	if _, err := NewRoutedServer(router, resolver, targets); err == nil {
		t.Fatal("an unreadable verifier must be rejected")
	}
	// a directly served backend ignores resolver updates
	direct, err := NewServer(testConfig(upstream))
	if err != nil {
		t.Fatal(err)
	}
	direct.UpdateRouteResolver(resolver)
	if direct.routes != nil {
		t.Fatal("a direct server must stay unrouted")
	}
}

type routerClient struct {
	backends.Backend
	resolver backends.RouteResolver
}

func (c *routerClient) RouteResolver() backends.RouteResolver { return c.resolver }

func routedAdapterConfig() (*bo.Options, *bo.Options) {
	router := configTestOptions()
	router.Name, router.Provider, router.OriginURL = routeTestRouter, providers.ALB, ""
	router.ListenerNames = []string{configTestName}
	router.ALBOptions = &albopt.Options{MechanismName: names.MechanismUR, UserRouter: &options.Options{
		DefaultBackend: routeTestBeta, TargetProvider: providers.TimescaleDB,
		Users: options.UserMappingOptionsByUser{testClientUser: {ToBackend: routeTestAlpha}},
	}}
	target := configTestOptions()
	target.AuthenticatorName, target.AuthOptions = "", nil
	return router, target
}

func TestNativeListenerAdapterUserRouter(t *testing.T) {
	adapter := NewNativeListenerAdapter(NewEngines(testEngine{}))
	router, target := routedAdapterConfig()
	c := adapterTestConfig()
	alpha, beta := target.Clone(), target.Clone()
	alpha.Name, beta.Name = routeTestAlpha, routeTestBeta
	c.Backends = bo.Lookup{routeTestRouter: router, routeTestAlpha: alpha, routeTestBeta: beta}
	if err := adapter.ValidateUserRouter(c, routeTestRouter, router); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*bo.Options, *bo.Lookup){
		"no router options": func(o *bo.Options, _ *bo.Lookup) { o.ALBOptions = nil },
		"another mechanism": func(o *bo.Options, _ *bo.Lookup) { o.ALBOptions.MechanismName = names.MechanismRR },
		"no authenticator":  func(o *bo.Options, _ *bo.Lookup) { o.AuthenticatorName = "" },
		"no users":          func(o *bo.Options, _ *bo.Lookup) { o.AuthOptions.Users = nil },
		"no routes": func(o *bo.Options, _ *bo.Lookup) {
			o.ALBOptions.UserRouter.DefaultBackend, o.ALBOptions.UserRouter.Users = "", nil
		},
		"nil mapping": func(o *bo.Options, _ *bo.Lookup) { o.ALBOptions.UserRouter.Users[testClientUser] = nil },
		"credential replacement": func(o *bo.Options, _ *bo.Lookup) {
			o.ALBOptions.UserRouter.Users[testClientUser].ToUser = testUpstreamUser
		},
		"empty target":   func(o *bo.Options, _ *bo.Lookup) { o.ALBOptions.UserRouter.Users[testClientUser].ToBackend = "" },
		"missing target": func(_ *bo.Options, l *bo.Lookup) { delete(*l, routeTestBeta) },
		"target of another provider": func(_ *bo.Options, l *bo.Lookup) {
			(*l)[routeTestBeta].Provider = providers.MySQL
		},
		"invalid target": func(_ *bo.Options, l *bo.Lookup) { (*l)[routeTestBeta].OriginURL = "://bad" },
		"target without origin credentials": func(_ *bo.Options, l *bo.Lookup) {
			(*l)[routeTestBeta].OriginURL = "postgres://db.example/trickster"
		},
	} {
		broken := c.Clone()
		mutate(broken.Backends[routeTestRouter], &broken.Backends)
		if err := adapter.ValidateUserRouter(broken, routeTestRouter, broken.Backends[routeTestRouter]); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}

	descriptor, err := adapter.Describe(c, configTestName)
	if err != nil || descriptor.RestartKey == "" {
		t.Fatalf("Describe() = %+v, %v", descriptor, err)
	}
	// sessions are pinned to a target, so a change to any target restarts the listener
	moved := c.Clone()
	moved.Backends[routeTestBeta].OriginURL = "postgres://origin:origin-password@elsewhere.example/trickster"
	if changed, err := adapter.Describe(moved, configTestName); err != nil || changed.RestartKey == descriptor.RestartKey {
		t.Fatalf("a target change must change the restart key: %v", err)
	}
	rerouted := c.Clone()
	rerouted.Backends[routeTestRouter].ALBOptions.UserRouter.Users[routeTestOther] = &options.UserMappingOptions{ToBackend: routeTestBeta}
	if changed, err := adapter.Describe(rerouted, configTestName); err != nil || changed.RestartKey == descriptor.RestartKey {
		t.Fatalf("a route change must change the restart key: %v", err)
	}

	request := native.BuildRequest{Config: c, ListenerName: configTestName, Listener: c.Listeners[configTestName]}
	if _, err = adapter.Build(request); err == nil {
		t.Fatal("Build() without a router client must fail")
	}
	if adapter.RouteResolver(request) != nil {
		t.Fatal("expected no resolver without a router client")
	}
	resolver := &stubResolver{}
	request.BackendClients = backends.Backends{
		routeTestRouter: &routerClient{Backend: routeTestBackend(t, routeTestRouter), resolver: resolver},
		routeTestAlpha:  routeTestBackend(t, routeTestAlpha),
	}
	if adapter.RouteResolver(request) != backends.RouteResolver(resolver) {
		t.Fatal("expected the router client's resolver")
	}
	server, err := adapter.Build(request)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	routed := server.(*Server)
	if routed.routes == nil || len(routed.routes.targets) != 2 || routed.routes.targets[routeTestAlpha].config.CacheProvider == nil {
		t.Fatalf("unexpected route table %+v", routed.routes)
	}
	if err = server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	request.Config = moved.Clone()
	request.Config.Backends[routeTestBeta].OriginURL = "://bad"
	if _, err = adapter.Build(request); err == nil {
		t.Fatal("Build() with an invalid target must fail")
	}
}
