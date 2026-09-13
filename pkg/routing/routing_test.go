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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	logmgr "github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/stdout"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
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
		map[string]string{headers.NameContentType: "application/json"})
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

	opts := &options.Options{MechanismName: names.MechanismTSM}
	opts.OutputFormat = providers.Prometheus
	conf.Backends["default"].ALBOptions = opts

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
	if code := serve(oo.Router, "/results/42", ""); code != http.StatusOK {
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
func TestPassthroughLaneSelection(t *testing.T) {
	conf := config.NewConfig()
	o := conf.Backends["default"]
	o.Provider = providers.ReverseProxy
	o.OriginURL = "http://example.com"

	client, err := reverseproxy.NewClient("default", o, lm.NewRouter(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.Paths = client.DefaultPathConfigs(o).Overlay(o.Paths)
	RegisterPathRoutes(lm.NewRouter(), conf, client.Handlers(), client, o, nil, nil)

	if len(o.Paths) != 1 {
		t.Fatalf("expected one default path, got %d", len(o.Paths))
	}
	p := o.Paths[0]
	if !p.HandlerFromRegistry {
		t.Error("a handler resolved from the registry must be marked as such")
	}
	if !isPassthroughPath(p) {
		t.Errorf("reverseproxy %q path should route to the passthrough lane", p.HandlerName)
	}

	// a handler assigned directly is left alone: the name no longer describes it
	direct := po.New()
	direct.HandlerName = providers.Proxy
	direct.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if isPassthroughPath(direct) {
		t.Error("a directly-assigned handler must not be replaced by the passthrough lane")
	}
	if isPassthroughPath(nil) {
		t.Error("nil path options must not select the passthrough lane")
	}
}

func TestRegisterPathRoutesRegexCaptures(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	oo := conf.Backends["default"]
	oo.Hosts = []string{"example.com"}
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)

	var seenPath string
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	hl := handlers.Lookup{"capture": capture}
	strip, err := rewriter.ParseRewriteList(rwopts.RewriteList{[]string{"path", "set", "/${2}"}})
	if err != nil {
		t.Fatal(err)
	}
	p := po.New()
	p.Path = `^/app(/|$)(.*)`
	p.HandlerName = "capture"
	p.Methods = []string{http.MethodGet}
	p.ReqRewriter = strip
	if err := p.Initialize(""); err != nil {
		t.Fatal(err)
	}
	oo.Paths = po.List{p}
	rtr := lm.NewRouter()
	RegisterPathRoutes(rtr, conf, hl, rpc, oo, nil, nil)

	serve := func(path, host string) int {
		seenPath = ""
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if host != "" {
			req.Host = host
		}
		w := httptest.NewRecorder()
		rtr.ServeHTTP(w, req)
		return w.Code
	}
	// host routing: captures come from the request path as sent
	if code := serve("/app/x/y", "example.com"); code != http.StatusOK || seenPath != "/x/y" {
		t.Fatalf("host-routed capture: status %d path %q; want 200 /x/y", code, seenPath)
	}
	// path routing: captures come from the un-stripped /default/app/... path
	if code := serve("/default/app/x/y", ""); code != http.StatusOK || seenPath != "/x/y" {
		t.Fatalf("path-routed capture: status %d path %q; want 200 /x/y", code, seenPath)
	}
	if code := serve("/app", "example.com"); code != http.StatusOK || seenPath != "/" {
		t.Fatalf("empty capture: status %d path %q; want 200 /", code, seenPath)
	}
}

func TestNewAccessLoggerInheritsDefault(t *testing.T) {
	conf := config.NewConfig()
	conf.AccessLog = &alo.Options{Filename: logmgr.StreamStdout}
	o := conf.Backends["default"]
	o.Name = "default"
	o.Provider = providers.ReverseProxyShort
	al := newAccessLogger(conf, o)
	if al == nil {
		t.Fatal("backend without access_log must inherit the default")
	}
	al.Close()
	o.AccessLog = &alo.Options{}
	if newAccessLogger(conf, o) != nil {
		t.Error("an explicit empty access_log must disable logging for the backend")
	}
	if newAccessLogger(conf, nil) != nil {
		t.Error("nil backend must yield no logger")
	}
	rl := RouterAccessLogger(conf)
	if rl == nil {
		t.Fatal("expected a router-level logger from the default access_log")
	}
	rl.Close()
	conf.AccessLog = &alo.Options{Filename: logmgr.StreamStdout, Format: "%{nope}x"}
	o.AccessLog = nil
	if RouterAccessLogger(conf) != nil || newAccessLogger(conf, o) != nil {
		t.Error("an invalid default format must disable both loggers")
	}
	if RouterAccessLogger(nil) != nil {
		t.Error("nil config must yield no logger")
	}
}

func TestRegisterDefaultBackendRoutesRegexCaptures(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	oo := conf.Backends["default"]
	oo.IsDefault = true
	oo.ListenerNames = []string{"default"}
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)

	var seenPath, seenTenant string
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenTenant = r.Header.Get("X-Tenant")
		w.WriteHeader(http.StatusOK)
	})
	strip, err := rewriter.ParseRewriteList(rwopts.RewriteList{
		[]string{"path", "set", "/${rest}"},
		[]string{"header", "set", "X-Tenant", "${1}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := po.New()
	p.Path = `^/(?P<tenant>[a-z]+)/app/(?P<rest>.*)`
	p.HandlerName = "capture"
	p.Handler = capture
	p.Methods = []string{http.MethodGet}
	p.ReqRewriter = strip
	if err := p.Initialize(""); err != nil {
		t.Fatal(err)
	}
	oo.Paths = po.List{p}
	routers := map[string]router.Router{"default": lm.NewRouter()}
	RegisterDefaultBackendRoutesForListeners(routers, conf, backends.Backends{"default": rpc}, nil)

	req := httptest.NewRequest(http.MethodGet, "/acme/app/x/y", nil)
	w := httptest.NewRecorder()
	routers["default"].ServeHTTP(w, req)
	if w.Code != http.StatusOK || seenPath != "/x/y" || seenTenant != "acme" {
		t.Fatalf("default-backend captures: status %d path %q tenant %q; want 200 /x/y acme",
			w.Code, seenPath, seenTenant)
	}
}

func TestDefaultAccessLogSkipsOptedOutBackend(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	logPath := filepath.Join(t.TempDir(), "default.access.log")
	conf.AccessLog = &alo.Options{Filename: logPath, Format: "%U %>s %{backend}x"}
	oo := conf.Backends["default"]
	oo.Hosts = []string{"example.com"}
	oo.AccessLog = &alo.Options{}
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	hl := handlers.Lookup{"ok": ok}
	p := po.New()
	p.Path = "/served"
	p.HandlerName = "ok"
	p.Methods = []string{http.MethodGet}
	if err := p.Initialize(""); err != nil {
		t.Fatal(err)
	}
	oo.Paths = po.List{p}
	rtr := lm.NewRouter()
	RegisterPathRoutes(rtr, conf, hl, rpc, oo, nil, nil)
	rl := RouterAccessLogger(conf)
	if rl == nil {
		t.Fatal("expected a router-level logger")
	}
	h := accesslog.RouterMiddleware(rl, rtr)
	for _, path := range []string{"/default/served", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	rl.Close()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 || lines[0] != "/nope 404 "+accesslog.UnmatchedName {
		t.Fatalf("lines = %q; want only the unmatched request logged", lines)
	}
}

const (
	generatedHost      = "shop.example.com"
	generatedOtherHost = "other.example.com"
	generatedPath      = "/api"
	generatedOffPath   = "/admin"
	generatedBackend   = "shop"
)

// generatedBackendConfig builds a config holding one non-default backend that
// proxies to origin and carries a single prefix path, the shape the
// Kubernetes compiler emits for one route rule.
func generatedBackendConfig(t *testing.T, origin string) (*config.Config, *bo.Options) {
	t.Helper()
	conf, err := config.Load([]string{"-origin-url", origin, "-provider", providers.ReverseProxyShort})
	if err != nil {
		t.Fatal(err)
	}
	o := conf.Backends["default"]
	// a backend named "default" is promoted to is_default, which registers
	// root routes of its own and would mask what these tests assert
	delete(conf.Backends, "default")
	o.Name = generatedBackend
	conf.Backends[generatedBackend] = o
	p := po.New()
	p.Path = generatedPath
	p.MatchTypeName = matching.PathMatchNamePrefix
	p.HandlerName = providers.Proxy
	p.Methods = []string{http.MethodGet}
	if err := p.Initialize(""); err != nil {
		t.Fatal(err)
	}
	o.Paths = po.List{p}
	return conf, o
}

func registerGeneratedBackend(t *testing.T, conf *config.Config, o *bo.Options) router.Router {
	t.Helper()
	client, err := reverseproxy.NewClient(o.Name, o, lm.NewRouter(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	caches := registry.LoadCachesFromConfig(conf)
	t.Cleanup(func() { registry.CloseCaches(caches) })
	rtr := lm.NewRouter()
	if err := RegisterProxyRoutes(conf, backends.Backends{o.Name: client},
		rtr, nil, caches, nil, false); err != nil {
		t.Fatal(err)
	}
	return rtr
}

func getGenerated(rtr router.Router, host, path string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code
}

func TestPathDefaultsDisabledLimitsRoutesToConfiguredPaths(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	// without the option the provider's own "/" catch-all is merged in and
	// answers paths the configured route never attached
	conf, o := generatedBackendConfig(t, origin.URL)
	o.Hosts = []string{generatedHost}
	o.PathRoutingDisabled = true
	if code := getGenerated(registerGeneratedBackend(t, conf, o), generatedHost, generatedOffPath); code != http.StatusOK {
		t.Fatalf("provider catch-all status = %d; want 200, so this test can detect its removal", code)
	}

	conf, o = generatedBackendConfig(t, origin.URL)
	o.Hosts = []string{generatedHost}
	o.PathRoutingDisabled = true
	o.PathDefaultsDisabled = true
	rtr := registerGeneratedBackend(t, conf, o)
	before := hits.Load()
	if code := getGenerated(rtr, generatedHost, generatedPath); code != http.StatusOK {
		t.Errorf("configured path status = %d; want 200", code)
	}
	if code := getGenerated(rtr, generatedHost, generatedOffPath); code != http.StatusNotFound {
		t.Errorf("unattached path status = %d; want 404", code)
	}
	if got := hits.Load() - before; got != 1 {
		t.Errorf("origin hits = %d; want only the configured path to reach the backend", got)
	}
}

func TestAnyHostRoutingRegistersHostlessRoutes(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	// a hostless backend with path routing disabled registers nothing at all
	conf, o := generatedBackendConfig(t, origin.URL)
	o.PathRoutingDisabled = true
	o.PathDefaultsDisabled = true
	if code := getGenerated(registerGeneratedBackend(t, conf, o), generatedHost, generatedPath); code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404, so this test can detect the missing registration", code)
	}

	conf, o = generatedBackendConfig(t, origin.URL)
	o.PathRoutingDisabled = true
	o.PathDefaultsDisabled = true
	o.AnyHostRouting = true
	rtr := registerGeneratedBackend(t, conf, o)
	for _, host := range []string{generatedHost, generatedOtherHost} {
		if code := getGenerated(rtr, host, generatedPath); code != http.StatusOK {
			t.Errorf("host %q status = %d; want the route to serve every hostname", host, code)
		}
	}
	if code := getGenerated(rtr, generatedHost, generatedOffPath); code != http.StatusNotFound {
		t.Errorf("unattached path status = %d; want 404", code)
	}
	if code := getGenerated(rtr, generatedHost, "/"+generatedBackend+generatedPath); code != http.StatusNotFound {
		t.Errorf("backend-name path status = %d; want it to stay unexposed", code)
	}
}

// A dispatch-only path is reachable through an ALB pool or a rule's
// next_route, so it must live on the backend's own router and on no listener:
// neither under the backend's hosts nor under its path-routing prefix
func TestRegisterPathRoutesDispatchOnly(t *testing.T) {
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

	newPath := func(path string, dispatchOnly bool) *po.Options {
		p := po.New()
		p.Path = path
		p.MatchTypeName = matching.PathMatchNameExact
		p.HandlerName = "testHandler"
		p.Methods = []string{http.MethodGet}
		p.DispatchOnly = dispatchOnly
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oo.Paths = po.List{newPath("/open", false), newPath("/internal", true)}
	rtr := lm.NewRouter()
	RegisterPathRoutes(rtr, conf, hl, rpc, oo, nil, nil)

	serve := func(r router.Router, path, host string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if host != "" {
			req.Host = host
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := serve(rtr, "/open", "example.com"); code != http.StatusOK {
		t.Fatalf("expected 200 for the ordinary path, got %d", code)
	}
	if code := serve(rtr, "/default/open", ""); code != http.StatusOK {
		t.Fatalf("expected 200 for the ordinary path under path routing, got %d", code)
	}
	if code := serve(rtr, "/internal", "example.com"); code != http.StatusNotFound {
		t.Fatalf("a dispatch-only path must not register under the hosts, got %d", code)
	}
	if code := serve(rtr, "/default/internal", ""); code != http.StatusNotFound {
		t.Fatalf("a dispatch-only path must not register under path routing, got %d", code)
	}
	if code := serve(oo.Router, "/internal", ""); code != http.StatusOK {
		t.Fatalf("a dispatch-only path must be served by the backend's own router, got %d", code)
	}
}

func TestRegisterPathRoutesConditioned(t *testing.T) {
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
	gold := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	plain := http.HandlerFunc(testutil.BasicHTTPHandler)
	hl := handlers.Lookup{"gold": gold, "plain": plain}

	newPath := func(path, handler string, mt matching.PathMatchName, hdrs []*po.Condition) *po.Options {
		p := po.New()
		p.Path = path
		p.MatchTypeName = mt
		p.HandlerName = handler
		p.Methods = []string{http.MethodGet}
		p.MatchHeaders = hdrs
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// the conditioned path is declared first, so it is tried first
	oo.Paths = po.List{
		newPath("/api", "gold", matching.PathMatchNameSegment,
			[]*po.Condition{{Name: "X-Tenant", Value: "gold"}}),
		newPath("/api", "plain", matching.PathMatchNameSegment, nil),
	}
	rtr := lm.NewRouter()
	RegisterPathRoutes(rtr, conf, hl, rpc, oo, nil, nil)

	serve := func(r router.Router, path, host, tenant string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if host != "" {
			req.Host = host
		}
		if tenant != "" {
			req.Header.Set("X-Tenant", tenant)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	for _, r := range []router.Router{rtr, rpc.Router().(router.Router)} {
		host := "example.com"
		if r != rtr {
			host = ""
		}
		if code := serve(r, "/api/items", host, "gold"); code != http.StatusAccepted {
			t.Fatalf("expected the conditioned path for the gold tenant, got %d", code)
		}
		if code := serve(r, "/api/items", host, "silver"); code != http.StatusOK {
			t.Fatalf("expected the unconditioned path for another tenant, got %d", code)
		}
		if code := serve(r, "/apiary", host, "gold"); code != http.StatusNotFound {
			t.Fatalf("expected a segment miss to be not found, got %d", code)
		}
	}
	if code := serve(rtr, "/default/api/items", "", "gold"); code != http.StatusAccepted {
		t.Fatalf("expected the conditioned path under path routing, got %d", code)
	}
}

func TestRegisterDefaultBackendRoutesSegment(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	oo := conf.Backends["default"]
	oo.IsDefault = true
	oo.PathDefaultsDisabled = true
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)
	p := po.New()
	p.Path = "/seg"
	p.MatchTypeName = matching.PathMatchNameSegment
	p.HandlerName = "testHandler"
	p.Handler = http.HandlerFunc(testutil.BasicHTTPHandler)
	p.Methods = []string{http.MethodGet}
	if err := p.Initialize(""); err != nil {
		t.Fatal(err)
	}
	oo.Paths = po.List{p}
	rtr := lm.NewRouter()
	RegisterDefaultBackendRoutes(rtr, conf, backends.Backends{"default": rpc}, nil)
	for path, want := range map[string]int{"/seg": http.StatusOK, "/seg/x": http.StatusOK,
		"/segment": http.StatusNotFound} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		rtr.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("%s: expected %d got %d", path, want, w.Code)
		}
	}
}

func TestRegisterPathRoutesMirror(t *testing.T) {
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
	shadowOpts := bo.New()
	shadowOpts.Name = "shadow"
	shadow, _ := reverseproxycache.NewClient("shadow", shadowOpts, lm.NewRouter(), nil, nil, nil)
	mirrored := make(chan string, 4)
	shadowHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrored <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	hl := handlers.Lookup{"testHandler": http.HandlerFunc(testutil.BasicHTTPHandler), "shadow": shadowHandler}

	newPath := func(path, handler string, mirror *po.MirrorOptions) *po.Options {
		p := po.New()
		p.Path = path
		p.MatchTypeName = matching.PathMatchNameExact
		p.HandlerName = handler
		p.Methods = []string{http.MethodGet}
		if mirror != nil {
			p.Mirrors = []*po.MirrorOptions{mirror}
		}
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
		return p
	}
	shadowOpts.Paths = po.List{newPath("/api", "shadow", nil)}
	registerPathRoutes([]listenerRoute{{lm.NewRouter(), nil}}, conf, hl, shadow, shadowOpts, nil, nil, nil)

	oo.Paths = po.List{
		newPath("/api", "testHandler", &po.MirrorOptions{BackendName: "shadow"}),
		newPath("/lonely", "testHandler", &po.MirrorOptions{BackendName: "missing"}),
	}
	rtr := lm.NewRouter()
	clients := backends.Backends{"default": rpc, "shadow": shadow}
	registerPathRoutes([]listenerRoute{{rtr, nil}}, conf, hl, rpc, oo, nil, nil, clients)

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for the mirrored path, got %d", w.Code)
	}
	select {
	case path := <-mirrored:
		if path != "/api" {
			t.Errorf("mirrored path = %q; want /api", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request was not mirrored to the shadow backend")
	}
	// a path whose mirror backend is unavailable is served without one
	req = httptest.NewRequest(http.MethodGet, "/lonely", nil)
	req.Host = "example.com"
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for the path without a mirror, got %d", w.Code)
	}
}

func TestDefaultBackendRoutesMirror(t *testing.T) {
	// the default backend serves the hosts no other backend claims, and a mirror configured on
	// its paths fires there too
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	conf, err := config.Load([]string{
		"-origin-url", "http://1", "-provider", providers.ReverseProxyCacheShort,
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	oo := conf.Backends["default"]
	oo.IsDefault = true
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)
	shadowOpts := bo.New()
	shadowOpts.Name = "shadow"
	shadow, _ := reverseproxycache.NewClient("shadow", shadowOpts, lm.NewRouter(), nil, nil, nil)
	mirrored := make(chan string, 4)
	hl := handlers.Lookup{
		"testHandler": http.HandlerFunc(testutil.BasicHTTPHandler),
		"shadow": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mirrored <- r.URL.Path
			w.WriteHeader(http.StatusOK)
		}),
	}
	newPath := func(path, handler string, mirror *po.MirrorOptions) *po.Options {
		p := po.New()
		p.Path = path
		p.MatchTypeName = matching.PathMatchNamePrefix
		p.HandlerName = handler
		p.Methods = []string{http.MethodGet}
		if mirror != nil {
			p.Mirrors = []*po.MirrorOptions{mirror}
		}
		if err := p.Initialize(""); err != nil {
			t.Fatal(err)
		}
		return p
	}
	shadowOpts.Paths = po.List{newPath("/", "shadow", nil)}
	registerPathRoutes([]listenerRoute{{lm.NewRouter(), nil}}, conf, hl, shadow, shadowOpts, nil, nil, nil)
	oo.Paths = po.List{newPath("/", "testHandler", &po.MirrorOptions{BackendName: "shadow"})}
	for _, p := range oo.Paths {
		p.Handler = hl[p.HandlerName]
	}
	rtr := lm.NewRouter()
	RegisterDefaultBackendRoutes(rtr, conf, backends.Backends{"default": rpc, "shadow": shadow}, nil)

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Host = "unclaimed.example.com"
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from the default backend, got %d", w.Code)
	}
	select {
	case path := <-mirrored:
		if path != "/anything" {
			t.Errorf("mirrored path = %q; want /anything", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the default backend's request was not mirrored")
	}
}
