/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package registration

import (
	"testing"

	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

func TestRegisterProxyRoutes(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-log-level", "debug", "-origin-url", "http://1", "-origin-type", "prometheus"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	RegisterProxyRoutes()

	if len(ProxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

	config.Origins["default"] = config.NewOriginConfig()

	// Test Too Many Defaults
	o1 := config.Origins["default"]
	o2 := config.NewOriginConfig()

	o1.IsDefault = true
	o2.IsDefault = true

	o1.OriginType = "rpc"
	o2.OriginType = "rpc"

	config.Origins["2"] = o2

	err = RegisterProxyRoutes()
	if err == nil {
		t.Errorf("Expected error for too many default origins.%s", "")
	}

	o1.IsDefault = false
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

	o2.IsDefault = false
	o2.CacheName = "invalid"
	err = RegisterProxyRoutes()
	if err == nil {
		t.Errorf("Expected error for invalid cache name%s", "")
	}

	o2.CacheName = "default"
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

	// teset the condition where no origins are IsDefault true,
	// and no origins are named default

	o1.IsDefault = false
	o2.IsDefault = false
	config.Origins["1"] = o1
	delete(config.Origins, "default")

	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

}

func TestRegisterProxyRoutesInflux(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-log-level", "debug", "-origin-url", "http://1", "-origin-type", "influxdb"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

	if len(ProxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

}

func TestRegisterProxyRoutesClickHouse(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-log-level", "debug", "-origin-url", "http://1", "-origin-type", "clickhouse"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

	if len(ProxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

}

func TestRegisterProxyRoutesIRONdb(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-origin-url", "http://example.com", "-origin-type", "irondb", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

	if len(ProxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}
}

func TestRegisterProxyRoutesMultipleDefaults(t *testing.T) {
	expected1 := "only one origin can be marked as default. Found both test and test2"
	expected2 := "only one origin can be marked as default. Found both test2 and test"

	a := []string{"-config", "../../../testdata/test.too_many_defaults.conf"}
	err := config.Load("trickster", "test", a)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected1)
	} else if err.Error() != expected1 && err.Error() != expected2 {
		t.Errorf("expected error `%s` got `%s`", expected1, err.Error())
	}
}

func TestRegisterProxyRoutesInvalidCert(t *testing.T) {
	expected := "tls: failed to find any PEM data in certificate input"
	a := []string{"-config", "../../../testdata/test.bad_tls_cert.conf"}
	err := config.Load("trickster", "test", a)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err == nil {
		t.Errorf("expected error: %s", expected)
	}
	if err != nil && err.Error() != expected {
		t.Errorf("expected error: %s, got: %s", expected, err.Error())
	}
}

func TestRegisterProxyRoutesBadCacheName(t *testing.T) {
	expected := "invalid cache name [test2] provided in origin config [test]"
	a := []string{"-config", "../../../testdata/test.bad_cache_name.conf"}
	err := config.Load("trickster", "test", a)
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected)
	} else if err.Error() != expected {
		t.Errorf("expected error `%s` got `%s`", expected, err.Error())
	}
}

func TestRegisterProxyRoutesBadOriginType(t *testing.T) {
	expected := "unknown origin type in origin config. originName: test, originType: foo"
	a := []string{"-config", "../../../testdata/test.unknown_origin_type.conf"}
	err := config.Load("trickster", "test", a)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err == nil {
		t.Errorf("expected error `%s` got nothing", expected)
	} else if err.Error() != expected {
		t.Errorf("expected error `%s` got `%s`", expected, err.Error())
	}
}

func TestRegisterMultipleOrigins(t *testing.T) {
	a := []string{"-config", "../../../testdata/test.multiple_origins.conf"}
	err := config.Load("trickster", "test", a)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}
}

func TestRegisterMultipleOriginsPlusDefault(t *testing.T) {
	a := []string{"-config", "../../../testdata/test.multiple_origins_plus_default.conf"}
	err := config.Load("trickster", "test", a)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}
	if !config.Origins["default"].IsDefault {
		t.Errorf("expected origin %s.IsDefault to be true", "default")
	}
}
