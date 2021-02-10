/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"io/ioutil"
	"net/http"
	"os"
	"testing"

	"github.com/tricksterproxy/trickster/pkg/backends"
	oo "github.com/tricksterproxy/trickster/pkg/backends/options"
	"github.com/tricksterproxy/trickster/pkg/backends/reverseproxycache"
	"github.com/tricksterproxy/trickster/pkg/backends/rule"
	"github.com/tricksterproxy/trickster/pkg/cache/registration"
	"github.com/tricksterproxy/trickster/pkg/config"
	tl "github.com/tricksterproxy/trickster/pkg/logging"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	"github.com/tricksterproxy/trickster/pkg/tracing/exporters/zipkin"
	to "github.com/tricksterproxy/trickster/pkg/tracing/options"
	tlstest "github.com/tricksterproxy/trickster/pkg/util/testing/tls"

	"github.com/gorilla/mux"
)

func TestRegisterPprofRoutes(t *testing.T) {
	router := http.NewServeMux()
	log := tl.ConsoleLogger("info")
	RegisterPprofRoutes("test", router, log)
	r, _ := http.NewRequest("GET", "http://0/debug/pprof", nil)
	_, p := router.Handler(r)
	if p != "/debug/pprof/" {
		t.Error("expected pprof route path")
	}
}

func TestRegisterProxyRoutes(t *testing.T) {

	var proxyClients backends.Backends

	log := tl.ConsoleLogger("info")
	conf, _, err := config.Load("trickster", "test",
		[]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", "prometheus"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	proxyClients, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, log, false)
	if err != nil {
		t.Error(err)
	}
	z, err := zipkin.NewTracer(&to.Options{ServiceName: "test", CollectorURL: "http://1.2.3.4/"})
	if err != nil {
		t.Error(err)
	}
	tr := map[string]*tracing.Tracer{"test": z}
	oc := conf.Backends["default"]
	oc.TracingConfigName = "test"

	oc.Hosts = []string{"test", "test2"}

	registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	RegisterProxyRoutes(conf, mux.NewRouter(), caches, tr, log, false)

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

	conf.Backends["default"] = oo.New()

	// Test Too Many Defaults
	o1 := conf.Backends["default"]
	o2 := oo.New()

	o1.IsDefault = true
	o2.IsDefault = true

	o1.Provider = "rpc"
	o2.Provider = "rpc"

	conf.Backends["2"] = o2

	router := mux.NewRouter()
	_, err = RegisterProxyRoutes(conf, router, caches, tr, log, false)
	if err == nil {
		t.Error("Expected error for too many default backends.")
	}

	o1.IsDefault = false
	o1.CacheName = "invalid"
	_, err = RegisterProxyRoutes(conf, router, caches, tr, log, false)
	if err == nil {
		t.Errorf("Expected error for invalid cache name")
	}

	o1.CacheName = o2.CacheName
	_, err = RegisterProxyRoutes(conf, router, caches, tr, log, false)
	if err != nil {
		t.Error(err)
	}

	o2.IsDefault = false
	o2.CacheName = "invalid"
	_, err = RegisterProxyRoutes(conf, router, caches, tr, log, false)
	if err == nil {
		t.Errorf("Expected error for invalid cache name")
	}

	o2.CacheName = "default"
	_, err = RegisterProxyRoutes(conf, router, caches, tr, log, false)
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

	_, err = RegisterProxyRoutes(conf, router, caches, tr, log, false)
	if err != nil {
		t.Error(err)
	}

}

func TestRegisterProxyRoutesInflux(t *testing.T) {
	conf, _, err := config.Load("trickster", "test",
		[]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", "influxdb"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	proxyClients, err := RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

}

func TestRegisterProxyRoutesClickHouse(t *testing.T) {

	conf, _, err := config.Load("trickster", "test",
		[]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", "clickhouse"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	proxyClients, err := RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

}

func TestRegisterProxyRoutesIRONdb(t *testing.T) {

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "http://example.com", "-provider", "irondb", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	proxyClients, err := RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesWithReqRewriters(t *testing.T) {

	conf, _, err := config.Load("trickster", "test",
		[]string{"-config", "../../testdata/test.routing.req_rewriter.conf"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	tpo := po.New()
	tpo.ReqRewriterName = "path"
	conf.Backends["test"].Paths["test"] = tpo

	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	proxyClients, err := RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err != nil {
		t.Error(err)
	}

	if len(proxyClients) != 2 {
		t.Errorf("expected %d got %d", 1, len(proxyClients))
	}
}

func TestRegisterProxyRoutesMultipleDefaults(t *testing.T) {
	expected1 := "only one backend can be marked as default. Found both test and test2"
	expected2 := "only one backend can be marked as default. Found both test2 and test"

	a := []string{"-config", "../../testdata/test.too_many_defaults.conf"}
	conf, _, err := config.Load("trickster", "test", a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	_, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected1)
	} else if err.Error() != expected1 && err.Error() != expected2 {
		t.Errorf("expected error `%s` got `%s`", expected1, err.Error())
	}
}

func TestRegisterProxyRoutesInvalidCert(t *testing.T) {
	expected := "tls: failed to find any PEM data in certificate input"

	kb, _, _ := tlstest.GetTestKeyAndCert(false)
	const certfile = "../../testdata/test.06.cert.pem"
	const keyfile = "../../testdata/test.06.key.pem"
	err := ioutil.WriteFile(certfile, []byte{}, 0600)
	if err != nil {
		t.Error(err)
	} else {
		defer os.Remove(certfile)
	}
	err = ioutil.WriteFile(keyfile, kb, 0600)
	if err != nil {
		t.Error(err)
	} else {
		defer os.Remove(keyfile)
	}

	a := []string{"-config", "../../testdata/test.bad_tls_cert.routes.conf"}
	conf, _, err := config.Load("trickster", "test", a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	_, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err == nil {
		t.Errorf("expected error: %s", expected)
	}
	if err != nil && err.Error() != expected {
		t.Errorf("expected error: %s, got: %s", expected, err.Error())
	}
}

func TestRegisterProxyRoutesBadCacheName(t *testing.T) {
	expected := "invalid cache name [test2] provided in backend options [test]"
	a := []string{"-config", "../../testdata/test.bad_cache_name.conf"}
	_, _, err := config.Load("trickster", "test", a)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected)
	} else if err.Error() != expected {
		t.Errorf("expected error `%s` got `%s`", expected, err.Error())
	}
}

func TestRegisterProxyRoutesBadProvider(t *testing.T) {
	expected := "unknown backend provider in backend options. backendName: test, backendProvider: foo"
	a := []string{"-config", "../../testdata/test.unknown_backend_type.conf"}
	conf, _, err := config.Load("trickster", "test", a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	_, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected)
	} else if err.Error() != expected {
		t.Errorf("expected error `%s` got `%s`", expected, err.Error())
	}
}

func TestRegisterMultipleBackends(t *testing.T) {
	a := []string{"-config", "../../testdata/test.multiple_backends.conf"}
	conf, _, err := config.Load("trickster", "test", a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	_, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err != nil {
		t.Error(err)
	}
}

func TestRegisterMultipleBackendsPlusDefault(t *testing.T) {
	a := []string{"-config", "../../testdata/test.multiple_backends_plus_default.conf"}
	conf, _, err := config.Load("trickster", "test", a)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}
	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)
	_, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err != nil {
		t.Error(err)
	}
	if !conf.Backends["default"].IsDefault {
		t.Errorf("expected backend %s.IsDefault to be true", "default")
	}
}

func TestRegisterPathRoutes(t *testing.T) {
	p := map[string]*po.Options{"test": {}}
	RegisterPathRoutes(nil, nil, nil, nil, nil, p, nil, "", nil)

	conf, _, err := config.Load("trickster", "test",
		[]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", "rpc"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oo := conf.Backends["default"]
	rpc, _ := reverseproxycache.NewClient("test", oo, mux.NewRouter(), nil)
	dpc := rpc.DefaultPathConfigs(oo)
	dpc["/-GET-HEAD"].Methods = nil
	RegisterPathRoutes(nil, nil, rpc, oo, nil, dpc, nil, "", tl.ConsoleLogger("INFO"))

}

func TestValidateRuleClients(t *testing.T) {

	c, err := rule.NewClient("test", nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	var cl = backends.Backends{"test": c}
	rule.ValidateOptions(cl, nil)

	conf, _, err := config.Load("trickster", "test",
		[]string{"-log-level", "debug", "-origin-url", "http://1", "-provider", "rpc"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := registration.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer registration.CloseCaches(caches)

	oc := conf.Backends["default"]
	oc.Provider = "rule"

	_, err = RegisterProxyRoutes(conf, mux.NewRouter(), caches, nil, tl.ConsoleLogger("info"), false)
	if err == nil {
		t.Error("expected error")
	}

}
