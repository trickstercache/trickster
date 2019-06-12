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

package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadEnvVars(t *testing.T) {

	os.Setenv(evOrigin, "http://1.1.1.1:9090")
	os.Setenv(evOriginType, "testing")
	os.Setenv(evProxyPort, "4001")
	os.Setenv(evMetricsPort, "4002")
	os.Setenv(evLogLevel, "info")

	a := []string{}
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

	d := Origins["default"]
	if d.OriginType != "testing" {
		t.Errorf("expected %s got %s", "testing", d.OriginType)
	}

	if ProxyServer.ListenPort != 4001 {
		t.Errorf("expected %d got %d", 4001, ProxyServer.ListenPort)
	}

	if Metrics.ListenPort != 4002 {
		t.Errorf("expected %d got %d", 4002, Metrics.ListenPort)
	}

	if d.Host != "1.1.1.1:9090" {
		t.Errorf("expected %s got %s", "1.1.1.1:9090", d.Host)
	}

	if strings.ToUpper(Logging.LogLevel) != "INFO" {
		t.Errorf("expected %s got %s", "INFO", Logging.LogLevel)
	}

	os.Unsetenv(evOrigin)
	os.Unsetenv(evOriginType)
	os.Unsetenv(evProxyPort)
	os.Unsetenv(evMetricsPort)
	os.Unsetenv(evLogLevel)

}
