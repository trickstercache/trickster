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

package backends

import (
	"net/http"
	"testing"

	bo "github.com/tricksterproxy/trickster/pkg/backends/options"
	cr "github.com/tricksterproxy/trickster/pkg/cache/registration"
	"github.com/tricksterproxy/trickster/pkg/config"
	tl "github.com/tricksterproxy/trickster/pkg/logging"
)

func TestConfiguration(t *testing.T) {

	oc := &bo.Options{Provider: "TEST"}
	client := &backend{config: oc}
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

// func TestHandlers(t *testing.T) {
// 	c, err := NewClient("test", nil, nil, nil, nil)
// 	if err != nil {
// 		t.Error(err)
// 	}
// 	m := c.Handlers()
// 	if _, ok := m["query"]; !ok {
// 		t.Errorf("expected to find handler named: %s", "query")
// 	}
// }
