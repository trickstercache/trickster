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

package routing

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxy"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/stdout"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	testutil "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func newPromClient() backends.Backend {
	promClient, _ := prometheus.NewClient("default", nil, lm.NewRouter(), nil, nil, nil)
	return promClient
}

var promClient = newPromClient()

func TestShouldCaptureAuthForVirtualBackend(t *testing.T) {
	path := po.New()
	backend := &bo.Options{Provider: providers.Rule}
	if !shouldCaptureAuth(path, backend) {
		t.Error("rule backend must capture downstream authentication")
	}
	backend.Provider = providers.ReverseProxyShort
	if shouldCaptureAuth(path, backend) {
		t.Error("ordinary unauthenticated backend should not seed resources")
	}
}

func TestRegisterHealthHandler(t *testing.T) {
	router := lm.NewRouter()
	path := "/test"
	hc := healthcheck.New()
	RegisterHealthHandler(router, path, hc, nil)
}

func TestRegisterProxyRoutes(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", providers.Prometheus})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	proxyClients := backends.Backends{"default": promClient}

	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches, nil, false)
	if err != nil {
		t.Error(err)
	}
	z, err := stdout.New(&to.Options{ServiceName: "test", Endpoint: "http://1.2.3.4/"})
	if err != nil {
		t.Error(err)
	}
	tr := tracing.Tracers{"test": z}
	o := conf.Backends["default"]
	o.TracingConfigName = "test"

	o.Hosts = []string{"test", "test2"}

	registry.LoadCachesFromConfig(conf)
	proxyClients = backends.Backends{"default": promClient}
	RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches, tr, false)

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

	conf.Backends["default"] = bo.New()

	// Test Too Many Defaults
	o1 := conf.Backends["default"]
	o2 := bo.New()

	o1.IsDefault = true
	o2.IsDefault = true

	o1.Provider = providers.ReverseProxyCacheShort
	o2.Provider = providers.ReverseProxyCacheShort

	conf.Backends["2"] = o2

	router := lm.NewRouter()
	proxyClients = backends.Backends{"default": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, router, lm.NewRouter(), caches, tr, false)
	if err == nil {
		t.Error("Expected error for too many default backends.")
	}

	o1.IsDefault = false
	o1.CacheName = "invalid"
	proxyClients = backends.Backends{"default": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, router, lm.NewRouter(), caches, tr, false)
	if err == nil {
		t.Errorf("Expected error for invalid cache name")
	}

	o1.CacheName = o2.CacheName
	proxyClients = backends.Backends{"default": promClient, "2": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, router, lm.NewRouter(), caches, tr, false)
	if err != nil {
		t.Error(err)
	}

	o2.IsDefault = false
	o2.CacheName = "invalid"
	proxyClients = make(backends.Backends)
	err = RegisterProxyRoutes(conf, proxyClients, router, lm.NewRouter(), caches, tr, false)
	if err == nil {
		t.Errorf("Expected error for invalid cache name")
	}

	o2.CacheName = "default"
	proxyClients = backends.Backends{"default": promClient, "2": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, router, lm.NewRouter(), caches, tr, false)
	if err != nil {
		t.Error(err)
	}

	// test the condition where no backends are IsDefault true,
	// and no backends are named default

	o1.IsDefault = false
	o2.IsDefault = false
	conf.Backends["1"] = o1
	delete(conf.Backends, "default")

	proxyClients = backends.Backends{"default": promClient, "1": promClient, "2": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, router, lm.NewRouter(), caches, tr, false)
	if err != nil {
		t.Error(err)
	}
}

func TestRegisterProxyRoutesInflux(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", providers.InfluxDB})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registry.LoadCachesFromConfig(conf)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	defer registry.CloseCaches(caches)
	influxClient, _ := influxdb.NewClient("default", nil, lm.NewRouter(), nil, nil, nil)
	proxyClients := backends.Backends{"default": influxClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesReverseProxy(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{
		"-log-level", "debug",
		"-origin-url", "http://1", "-provider", providers.ReverseProxyShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	rpClient, _ := reverseproxy.NewClient("default", nil, lm.NewRouter(), nil, nil, nil)
	proxyClients := backends.Backends{"default": rpClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesClickHouse(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", providers.ClickHouse})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	clickhouseClient, _ := clickhouse.NewClient("default", nil, lm.NewRouter(), nil, nil, nil)
	proxyClients := backends.Backends{"default": clickhouseClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesGraphite(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const body = `[{"target": "dev.fast.cpu.host01.percent", "datapoints": [[35.0, 1787343600]]}]`
	origin := testutil.NewTestServer(http.StatusOK, body,
		map[string]string{"Content-Type": "application/json"})
	defer origin.Close()

	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", origin.URL,
		"-provider", providers.Graphite})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))

	o := conf.Backends["default"]
	o.IsDefault = false // mount at /default/ only, not at /
	r := lm.NewRouter()
	graphiteClient, err := graphite.NewClient("default", o, r, caches["default"], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.HTTPClient = graphiteClient.HTTPClient()
	proxyClients := backends.Backends{"default": graphiteClient}
	err = RegisterProxyRoutes(conf, proxyClients, r, lm.NewRouter(), caches, nil, false)
	if err != nil {
		t.Error(err)
	}

	// /render goes to the render handler; the catch-all "/" proxies
	if len(o.Paths) < 2 {
		t.Fatalf("expected the full graphite path list, got %d", len(o.Paths))
	}
	if o.Paths[0].Path != "/render" || o.Paths[0].HandlerName != "render" {
		t.Errorf("expected /render -> render first, got %s -> %s", o.Paths[0].Path, o.Paths[0].HandlerName)
	}
	if last := o.Paths[len(o.Paths)-1]; last.Path != "/" || last.HandlerName != providers.Proxy {
		t.Errorf("expected path / to use the %s handler, got %s -> %s", providers.Proxy, last.Path, last.HandlerName)
	}

	// every graphite endpoint reaches the origin, including an unaccelerable
	// /render (the static origin body is not a learnable response)
	for _, path := range []string{
		"/default/render?target=dev.fast.cpu.host01.percent&from=-1h&format=json",
		"/default/metrics/find?query=dev.*",
		"/default/version",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected %d got %d", path, http.StatusOK, w.Code)
		}
		if w.Body.String() != body {
			t.Errorf("%s: expected origin body, got %q", path, w.Body.String())
		}
	}
}

func TestRegisterProxyRoutesALB(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", providers.ALB})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	conf.Backends["default"].ALBOptions = &options.Options{MechanismName: names.MechanismTSM, OutputFormat: providers.Prometheus}

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))

	albClient, _ := alb.NewClient("default", nil, lm.NewRouter(), nil, nil, nil)
	proxyClients := backends.Backends{"default": albClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesWithReqRewriters(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-config", "../../testdata/test.routing.req_rewriter.conf"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	tpo := po.New()
	tpo.ReqRewriterName = "path"
	conf.Backends["test"].Paths = append(conf.Backends["test"].Paths, tpo)

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	ruleClient, _ := rule.NewClient("test", nil, lm.NewRouter(), nil, nil, nil)
	proxyClients := backends.Backends{"test": ruleClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) != 2 {
		t.Errorf("expected %d got %d", 1, len(proxyClients))
	}
}

func TestRegisterProxyRoutesMultipleDefaults(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	expected1 := "only one backend can be marked as default. Found both test and test2"
	expected2 := "only one backend can be marked as default. Found both test2 and test"

	a := []string{"-config", "../../testdata/test.too_many_defaults.conf"}
	conf, err := config.Load(a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	proxyClients := make(backends.Backends)
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected1)
	} else if err.Error() != expected1 && err.Error() != expected2 {
		t.Errorf("expected error `%s` got `%s`", expected1, err.Error())
	}
}

func TestRegisterProxyRoutesBadProvider(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	expected := "unknown backend provider in backend options. backendName: test, backendProvider: foo"
	a := []string{"-config", "../../testdata/test.unknown_backend_provider.conf"}
	conf, err := config.Load(a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients := make(backends.Backends)
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected)
	} else if err.Error() != expected {
		t.Errorf("expected error `%s` got `%s`", expected, err.Error())
	}
}

func TestRegisterMultipleBackends(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	a := []string{"-config", "../../testdata/test.multiple_backends.conf"}
	conf, err := config.Load(a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients := backends.Backends{"test": promClient, "test2": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}
}

func TestRegisterMultipleBackendsPlusDefault(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	a := []string{"-config", "../../testdata/test.multiple_backends_plus_default.conf"}
	conf, err := config.Load(a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients := backends.Backends{"default": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}
	if !conf.Backends["default"].IsDefault {
		t.Errorf("expected backend %s.IsDefault to be true", "default")
	}
}

func TestRegisterPathRoutes(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	RegisterPathRoutes(nil, nil, nil, nil, nil, nil, nil)

	conf, err := config.Load([]string{
		"-log-level", "debug", "-origin-url",
		"http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	var i5000 int64 = 5000
	conf.Frontend.MaxRequestBodySizeBytes = &i5000

	oo := conf.Backends["default"]
	rpc, _ := reverseproxycache.NewClient("test", oo, lm.NewRouter(), nil, nil, nil)
	dpc := rpc.DefaultPathConfigs(oo)
	for _, pathConfig := range dpc {
		if pathConfig.Path == "/" && len(pathConfig.Methods) > 0 {
			pathConfig.Methods = nil
			break
		}
	}

	testHandler := http.HandlerFunc(testutil.BasicHTTPHandler)
	handlers := handlers.Lookup{"testHandler": testHandler}

	oo.Paths = dpc
	RegisterPathRoutes(nil, conf, handlers, rpc, oo, nil, nil)

	router := lm.NewRouter()
	dpc = rpc.DefaultPathConfigs(oo)
	oo.Paths = dpc
	// Find the path config with GET and HEAD methods and update it
	for _, pathConfig := range dpc {
		if pathConfig.Path == "/" && len(pathConfig.Methods) > 0 {
			pathConfig.Methods = []string{"*"}
			pathConfig.Handler = testHandler
			pathConfig.HandlerName = "testHandler"
			pathConfig.ReqRewriter = testutil.NewTestRewriteInstructions()
			break
		}
	}
	RegisterPathRoutes(router, conf, handlers, rpc, oo, nil, nil)
}

func TestRegisterPathRoutesRegex(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oo := conf.Backends["default"]
	oo.Hosts = []string{"example.com"}
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)

	testHandler := http.HandlerFunc(testutil.BasicHTTPHandler)
	hl := handlers.Lookup{"testHandler": testHandler}

	newRegexPaths := func() po.List {
		p := po.New()
		p.Path = "^/results/[0-9]+"
		p.HandlerName = "testHandler"
		p.Methods = []string{http.MethodGet}
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
		return po.List{p}
	}

	serve := func(r router.Router, path, host string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if host != "" {
			req.Host = host
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	oo.Paths = newRegexPaths()
	rtr := lm.NewRouter()
	RegisterPathRoutes(rtr, conf, hl, rpc, oo, nil, nil)

	// path-routing mode: the handledPath rewrite splices the backend name
	// into the pattern, so /default/results/42 matches ^/default/results/[0-9]+
	if code := serve(rtr, "/default/results/42", ""); code != http.StatusOK {
		t.Fatalf("expected 200 for path-routing regex, got %d", code)
	}
	if code := serve(rtr, "/default/results/notanumber", ""); code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-matching path, got %d", code)
	}

	// hosts registration passes the pattern through unmodified
	if code := serve(rtr, "/results/42", "example.com"); code != http.StatusOK {
		t.Fatalf("expected 200 for host-based regex, got %d", code)
	}

	// the backend-local router also receives the unmodified pattern
	if code := serve(oo.Router.(router.Router), "/results/42", ""); code != http.StatusOK {
		t.Fatalf("expected 200 for backend-local regex, got %d", code)
	}

	// path_routing_disabled skips the handledPath registration
	oo.PathRoutingDisabled = true
	oo.Paths = newRegexPaths()
	rtr = lm.NewRouter()
	RegisterPathRoutes(rtr, conf, hl, rpc, oo, nil, nil)
	if code := serve(rtr, "/default/results/42", ""); code != http.StatusNotFound {
		t.Fatalf("expected 404 with path routing disabled, got %d", code)
	}
	if code := serve(rtr, "/results/42", "example.com"); code != http.StatusOK {
		t.Fatalf("expected 200 for host-based regex, got %d", code)
	}
}

func TestRegisterPathRoutesRegexShadowWarning(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := logger.Logger()
	l := logging.StreamLogger(buf, level.Warn)
	l.SetLogAsynchronous(false)
	logger.SetLogger(l)
	t.Cleanup(func() { logger.SetLogger(prev) })

	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	oo := conf.Backends["default"]
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)
	testHandler := http.HandlerFunc(testutil.BasicHTTPHandler)
	hl := handlers.Lookup{"testHandler": testHandler}

	regex := po.New()
	regex.Path = "^/results/[0-9]+"
	regex.HandlerName = "testHandler"
	regex.Methods = []string{http.MethodGet}
	catchAll := po.New()
	catchAll.Path = "/"
	catchAll.MatchTypeName = matching.PathMatchNamePrefix
	catchAll.HandlerName = "testHandler"
	catchAll.Methods = []string{http.MethodGet}
	oo.Paths = po.List{regex, catchAll}
	if err := oo.Paths.Initialize(); err != nil {
		t.Fatal(err)
	}

	RegisterPathRoutes(lm.NewRouter(), conf, hl, rpc, oo, nil, nil)
	if !strings.Contains(buf.String(), "unreachable") {
		t.Fatalf("expected catch-all shadow warning in log, got %q", buf.String())
	}
}

func TestValidateRuleClients(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	c, err := rule.NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	cl := backends.Backends{"test": c}
	rule.ValidateOptions(cl, nil)

	conf, err := config.Load([]string{
		"-log-level", "debug", "-origin-url",
		"http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)

	o := conf.Backends["default"]
	o.Provider = providers.Rule

	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients := backends.Backends{"default": promClient}
	err = RegisterProxyRoutes(conf, proxyClients, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}
}

func TestRegisterDefaultBackendRoutes(t *testing.T) {
	// successful passing of this test is no panic

	r := lm.NewRouter()
	conf := config.NewConfig()
	oo := conf.Backends["default"]
	w := httptest.NewRecorder()
	l := logging.StreamLogger(w, level.Debug)
	logger.SetLogger(l)

	po1 := po.New()
	po1.Path = "/"
	po1.Handler = http.HandlerFunc(testutil.BasicHTTPHandler)
	po1.Methods = methods.GetAndPost()
	po1.MatchType = matching.PathMatchTypePrefix

	oo.TracingConfigName = "testTracer"
	oo.Paths = po.List{po1}
	oo.IsDefault = true
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)
	b := backends.Backends{"default": rpc}

	tr := tracing.Tracers{"testTracer": testutil.NewTestTracer()}

	ri := testutil.NewTestRewriteInstructions()
	oo.ReqRewriter = ri
	po1.ReqRewriter = ri
	RegisterDefaultBackendRoutes(r, conf, b, tr)

	r = lm.NewRouter()
	po1.MatchType = matching.PathMatchTypeExact
	RegisterDefaultBackendRoutes(r, conf, b, tr)

	logger.SetLogger(logging.ConsoleLogger(level.Info))
	l.Close()
}

func TestRegisterDefaultBackendRoutesForListeners(t *testing.T) {
	conf := config.NewConfig()
	o := conf.Backends["default"]
	o.ListenerNames = []string{"custom"}
	o.IsDefault = true
	p := po.New()
	p.Path = "/"
	p.Handler = http.HandlerFunc(testutil.BasicHTTPHandler)
	p.Methods = methods.GetAndPost()
	p.MatchType = matching.PathMatchTypePrefix
	o.Paths = po.List{p}

	client, err := reverseproxycache.NewClient("default", o, lm.NewRouter(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defaultRouter := lm.NewRouter()
	customRouter := lm.NewRouter()
	RegisterDefaultBackendRoutesForListeners(map[string]router.Router{
		listener.DefaultFrontendName: defaultRouter,
		"custom":                     customRouter,
	}, conf, backends.Backends{"default": client}, nil)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	customResponse := httptest.NewRecorder()
	customRouter.ServeHTTP(customResponse, request)
	if customResponse.Code != http.StatusOK {
		t.Errorf("custom listener status = %d, want %d", customResponse.Code, http.StatusOK)
	}
	defaultResponse := httptest.NewRecorder()
	defaultRouter.ServeHTTP(defaultResponse, request)
	if defaultResponse.Code == http.StatusOK {
		t.Errorf("backend route was also registered on default")
	}
}

func TestNewAccessLogger(t *testing.T) {
	if newAccessLogger(nil, nil) != nil {
		t.Error("expected nil logger for nil options")
	}
	o := bo.New()
	o.Name = "test"
	if newAccessLogger(nil, o) != nil {
		t.Error("expected nil logger when access logging is unconfigured")
	}
	o.AccessLog = &alo.Options{Filename: filepath.Join(t.TempDir(), "a.log")}
	al := newAccessLogger(nil, o)
	if al == nil {
		t.Fatal("expected an access logger")
	}
	al.Close()
	// formats are validated at config load; an invalid one here exercises
	// the construction failure branch
	o.AccessLog.Format = "%Z"
	if newAccessLogger(nil, o) != nil {
		t.Error("expected nil logger on construction error")
	}
}

func TestBackendRoutesOnMultipleHTTPListeners(t *testing.T) {
	conf := config.NewConfig()
	o := bo.New()
	o.Name = "shared"
	o.Provider = providers.ReverseProxyShort
	o.ListenerNames = []string{"small", "large"}
	o.IsDefault = true
	o.Hosts = []string{"origin.example"}
	conf.Backends = bo.Lookup{o.Name: o}
	routers := map[string]router.Router{}
	for _, name := range []string{"small", "large", "unused"} {
		lo := listener.New(name)
		limit := int64(64)
		if name == "small" {
			limit = 2
		}
		lo.MaxRequestBodySizeBytes = &limit
		conf.Listeners[name] = lo
		routers[name] = lm.NewRouter()
	}
	path := po.New()
	path.Path = "/echo"
	path.Methods = []string{http.MethodPost}
	path.MatchType = matching.PathMatchTypeExact
	path.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	o.Paths = po.List{path}
	client, err := reverseproxy.NewClient(o.Name, o, lm.NewRouter(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := backends.Backends{o.Name: client}
	if err := RegisterProxyRoutesForListeners(conf, clients, routers, lm.NewRouter(), nil, nil, false); err != nil {
		t.Fatal(err)
	}
	RegisterDefaultBackendRoutesForListeners(routers, conf, clients, nil)
	for _, name := range []string{"small", "large", "unused"} {
		for _, target := range []string{"/shared/echo", "/echo", "http://origin.example/echo"} {
			for _, body := range []string{"a", "abcd"} {
				req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
				rec := httptest.NewRecorder()
				routers[name].ServeHTTP(rec, req)
				accepted := name != "unused" && (name != "small" || len(body) <= 2)
				if (rec.Code == http.StatusAccepted) != accepted {
					t.Fatalf("%s %s body=%q: status %d", name, target, body, rec.Code)
				}
			}
		}
	}
	if clients[o.Name] != client || len(clients) != 2 {
		t.Fatal("duplicated backend clients for listener bindings")
	}
}
