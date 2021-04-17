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

package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadEnvVars(t *testing.T) {

	os.Setenv(evOriginURL, "http://1.1.1.1:9090/some/path")
	os.Setenv(evProvider, "testing")
	os.Setenv(evProxyPort, "4001")
	os.Setenv(evMetricsPort, "4002")
	os.Setenv(evLogLevel, "info")

	a := []string{}
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

	d := conf.Backends["default"]
	if d.Provider != "testing" {
		t.Errorf("expected %s got %s", "testing", d.Provider)
	}

	if conf.Frontend.ListenPort != 4001 {
		t.Errorf("expected %d got %d", 4001, conf.Frontend.ListenPort)
	}

	if conf.Metrics.ListenPort != 4002 {
		t.Errorf("expected %d got %d", 4002, conf.Metrics.ListenPort)
	}

	if d.Scheme != "http" {
		t.Errorf("expected %s got %s", "http", d.Scheme)
	}

	if d.Host != "1.1.1.1:9090" {
		t.Errorf("expected %s got %s", "1.1.1.1:9090", d.Host)
	}

	if d.PathPrefix != "/some/path" {
		t.Errorf("expected %s got %s", "/some/path", d.PathPrefix)
	}

	if strings.ToUpper(conf.Logging.LogLevel) != "INFO" {
		t.Errorf("expected %s got %s", "INFO", conf.Logging.LogLevel)
	}

	os.Unsetenv(evOriginURL)
	os.Unsetenv(evProvider)
	os.Unsetenv(evProxyPort)
	os.Unsetenv(evMetricsPort)
	os.Unsetenv(evLogLevel)

}
