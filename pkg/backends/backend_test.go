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

package backends

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registration"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

func TestConfiguration(t *testing.T) {

	o := &bo.Options{Provider: "TEST"}
	client := &backend{config: o}
	c := client.Configuration()
	if c.Provider != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c.Provider)
	}
}

func TestCache(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-provider", "influxdb", "-origin-url", "http://1"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}
	client := &backend{cache: cache}
	c := client.Cache()

	if c.Configuration().Provider != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Configuration().Provider)
	}
}

func TestName(t *testing.T) {

	client := &backend{name: "TEST"}
	c := client.Name()

	if c != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c)
	}

}

func TestRouter(t *testing.T) {
	client := &backend{name: "TEST"}
	r := client.Router()
	if r != nil {
		t.Error("expected nil router")
	}
}

func TestHTTPClient(t *testing.T) {
	o := &bo.Options{Provider: "TEST"}

	c := &backend{name: "test", config: o, webClient: &http.Client{}}

	if c.HTTPClient() == nil {
		t.Errorf("missing http client")
	}
}

func TestSetCache(t *testing.T) {

	c := &backend{name: "test", config: bo.New()}
	c.SetCache(nil)
	if c.Cache() != nil {
		t.Errorf("expected nil cache for client named %s", "test")
	}
}

func TestBaseUpstreamURL(t *testing.T) {

	u, _ := url.Parse("https://trickstercache.org/test")
	b := &backend{name: "test", baseUpstreamURL: u}
	u = b.BaseUpstreamURL()
	if u.Host != "trickstercache.org" || u.Scheme != "https" || u.Path != "/test" {
		t.Error("url mismatch")
	}
}

func TestHandlers(t *testing.T) {

	b := &backend{name: "test"}
	testRegistrar := func(map[string]http.Handler) {
		b.RegisterHandlers(map[string]http.Handler{"test": nil})
	}

	b.registrar = testRegistrar

	h := b.Handlers()
	if _, ok := h["test"]; !ok {
		t.Error("expected true")
	}

}

func TestSetHealthCheckProbe(t *testing.T) {
	b := &backend{name: "test"}
	b.SetHealthCheckProbe(nil)
	if b.healthProbe != nil {
		t.Error("expected nil")
	}
}

func TestHealthHandler(t *testing.T) {

	b := &backend{name: "test"}
	testProber := func(w http.ResponseWriter) {
		b.name = "trickster"
	}
	b.healthProbe = testProber
	b.HealthHandler(nil, nil)
	if b.name != "trickster" {
		t.Error("health probe failed")
	}
}

func TestDefaultPathConfigs(t *testing.T) {
	b := &backend{name: "test"}
	// should always return nil for the base Backend
	if b.DefaultPathConfigs(nil) != nil {
		t.Error("expected nil")
	}
}

func TestDefaultHealthCheckConfig(t *testing.T) {
	b := &backend{name: "test"}
	// should always return nil for the base Backend
	if b.DefaultHealthCheckConfig() != nil {
		t.Error("expected nil")
	}
}
