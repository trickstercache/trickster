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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/zipkin"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	testutil "github.com/trickstercache/trickster/v2/pkg/testutil"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestRegisterPprofRoutes(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	router := lm.NewRouter()
	RegisterPprofRoutes("test", router)
	r, _ := http.NewRequest("GET", "http://0/debug/pprof", nil)
	h := router.Handler(r)
	if h == nil {
		t.Error("expected non-nil handler")
	}
}

func TestRegisterHealthHandler(t *testing.T) {
	router := lm.NewRouter()
	path := "/test"
	hc := healthcheck.New()
	RegisterHealthHandler(router, path, hc)
}

func TestRegisterProxyRoutes(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	var proxyClients backends.Backends

	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", providers.Prometheus})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	proxyClients, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches, nil, false)
	if err != nil {
		t.Error(err)
	}
	z, err := zipkin.New(&to.Options{ServiceName: "test", Endpoint: "http://1.2.3.4/"})
	if err != nil {
		t.Error(err)
	}
	tr := tracing.Tracers{"test": z}
	o := conf.Backends["default"]
	o.TracingConfigName = "test"

	o.Hosts = []string{"test", "test2"}

	registry.LoadCachesFromConfig(conf)
	RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches, tr, false)

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
	_, err = RegisterProxyRoutes(conf, router, lm.NewRouter(), caches, tr, false)
	if err == nil {
		t.Error("Expected error for too many default backends.")
	}

	o1.IsDefault = false
	o1.CacheName = "invalid"
	_, err = RegisterProxyRoutes(conf, router, lm.NewRouter(), caches, tr, false)
	if err == nil {
		t.Errorf("Expected error for invalid cache name")
	}

	o1.CacheName = o2.CacheName
	_, err = RegisterProxyRoutes(conf, router, lm.NewRouter(), caches, tr, false)
	if err != nil {
		t.Error(err)
	}

	o2.IsDefault = false
	o2.CacheName = "invalid"
	_, err = RegisterProxyRoutes(conf, router, lm.NewRouter(), caches, tr, false)
	if err == nil {
		t.Errorf("Expected error for invalid cache name")
	}

	o2.CacheName = "default"
	_, err = RegisterProxyRoutes(conf, router, lm.NewRouter(), caches, tr, false)
	if err != nil {
		t.Error(err)
	}

	// test the condition where no backends are IsDefault true,
	// and no backends are named default

	o1.IsDefault = false
	o2.IsDefault = false
	conf.Backends["1"] = o1
	delete(conf.Backends, "default")

	o1.Paths["/-GET-HEAD"].Methods = nil

	_, err = RegisterProxyRoutes(conf, router, lm.NewRouter(), caches, tr, false)
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
	proxyClients, err := RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	conf, err := config.Load([]string{"-log-level", "debug",
		"-origin-url", "http://1", "-provider", providers.ReverseProxyShort})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients, err := RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	proxyClients, err := RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesALB(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", providers.ALB})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	conf.Backends["default"].ALBOptions = &options.Options{MechanismName: "tsm", OutputFormat: providers.Prometheus}

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients, err := RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	conf.Backends["test"].Paths["test"] = tpo

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	proxyClients, err := RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	_, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected1)
	} else if err.Error() != expected1 && err.Error() != expected2 {
		t.Errorf("expected error `%s` got `%s`", expected1, err.Error())
	}
}

func TestRegisterProxyRoutesInvalidCert(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	expected := "tls: failed to find any PEM data in certificate input"

	kb, _, _ := tlstest.GetTestKeyAndCert(false)

	td := t.TempDir()

	certfile := td + "/cert.pem"
	keyfile := td + "/key.pem"
	confFile := td + "/trickster_test_config.conf"

	err := os.WriteFile(certfile, []byte{}, 0600)
	if err != nil {
		t.Error(err)
	}
	err = os.WriteFile(keyfile, kb, 0600)
	if err != nil {
		t.Error(err)
	}

	b, err := os.ReadFile("../../testdata/test.bad_tls_cert.routes.conf")
	if err != nil {
		t.Error(err)
	}
	b = []byte(strings.ReplaceAll(string(b), `../../testdata/test.06.`, td+"/"))

	err = os.WriteFile(confFile, b, 0600)
	if err != nil {
		t.Error(err)
	}

	a := []string{"-config", confFile}
	conf, err := config.Load(a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)
	logger.SetLogger(logging.ConsoleLogger(level.Info))
	_, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
		nil, false)
	if err == nil {
		t.Errorf("expected error: %s", expected)
	}
	if err != nil && err.Error() != expected {
		t.Errorf("expected error: %s, got: %s", expected, err.Error())
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
	_, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	_, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	_, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	p := po.Lookup{"test": {}}
	RegisterPathRoutes(nil, nil, nil, nil, nil, p, nil)

	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url",
		"http://1", "-provider", providers.ReverseProxyCacheShort})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oo := conf.Backends["default"]
	rpc, _ := reverseproxycache.NewClient("test", oo, lm.NewRouter(), nil, nil, nil)
	dpc := rpc.DefaultPathConfigs(oo)
	dpc["/-GET-HEAD"].Methods = nil

	testHandler := http.HandlerFunc(testutil.BasicHTTPHandler)
	handlers := handlers.Lookup{"testHandler": testHandler}

	RegisterPathRoutes(nil, handlers, rpc, oo, nil, dpc, nil)

	router := lm.NewRouter()
	dpc = rpc.DefaultPathConfigs(oo)
	dpc["/-GET-HEAD"].Methods = []string{"*"}
	dpc["/-GET-HEAD"].Handler = testHandler
	dpc["/-GET-HEAD"].HandlerName = "testHandler"
	dpc["/-GET-HEAD"].ReqRewriter = testutil.NewTestRewriteInstructions()
	RegisterPathRoutes(router, handlers, rpc, oo, nil, dpc, nil)

}

func TestValidateRuleClients(t *testing.T) {

	logger.SetLogger(logging.ConsoleLogger(level.Error))
	c, err := rule.NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	var cl = backends.Backends{"test": c}
	rule.ValidateOptions(cl, nil)

	conf, err := config.Load([]string{"-log-level", "debug", "-origin-url",
		"http://1", "-provider", providers.ReverseProxyCacheShort})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registry.LoadCachesFromConfig(conf)
	defer registry.CloseCaches(caches)

	o := conf.Backends["default"]
	o.Provider = providers.Rule

	logger.SetLogger(logging.ConsoleLogger(level.Info))
	_, err = RegisterProxyRoutes(conf, lm.NewRouter(), lm.NewRouter(), caches,
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
	oo.Paths = po.Lookup{"root": po1}
	oo.IsDefault = true
	rpc, _ := reverseproxycache.NewClient("default", oo, lm.NewRouter(), nil, nil, nil)
	b := backends.Backends{"default": rpc}

	tr := tracing.Tracers{"testTracer": testutil.NewTestTracer()}

	ri := testutil.NewTestRewriteInstructions()
	oo.ReqRewriter = ri
	po1.ReqRewriter = ri
	RegisterDefaultBackendRoutes(r, b, tr)

	r = lm.NewRouter()
	po1.MatchType = matching.PathMatchTypeExact
	RegisterDefaultBackendRoutes(r, b, tr)

	logger.SetLogger(logging.ConsoleLogger(level.Info))
	l.Close()
}
