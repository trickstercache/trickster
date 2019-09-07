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

	err := config.Load("trickster", "test", []string{"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	registration.LoadCachesFromConfig()
	RegisterProxyRoutes()

	if len(ProxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

	config.Origins["default"] = config.DefaultOriginConfig()

	// Test Too Many Defaults
	o1 := config.Origins["default"]
	o2 := config.DefaultOriginConfig()

	o1.IsDefault = true
	o2.IsDefault = true

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

	err := config.Load("trickster", "test", []string{"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	do := config.Origins["default"]
	do.Type = "influxdb"
	config.Origins["default"] = do
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

	err := config.Load("trickster", "test", []string{"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	do := config.Origins["default"]
	do.Type = "irondb"
	config.Origins["default"] = do
	registration.LoadCachesFromConfig()
	err = RegisterProxyRoutes()
	if err != nil {
		t.Error(err)
	}

	if len(ProxyClients) == 0 {
		t.Errorf("expected %d got %d", 1, 0)
	}

}
