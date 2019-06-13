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

package prometheus

import (
	"io/ioutil"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func init() {
	metrics.Init()
}

func TestHealthHandler(t *testing.T) {

	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "prometheus"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/health", nil)

	client := &Client{name: "default", config: config.Origins["default"], webClient: tu.NewTestWebClient()}
	client.HealthHandler(w, r)
	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}

}

func TestHealthHandlerCustomPath(t *testing.T) {

	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	a := []string{"-config", "../../../../testdata/test.custom_health.conf"}
	err := config.Load("trickster", "test", a)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	c := config.Origins["test"]
	c.OriginURL = es.URL

	u, _ := url.Parse(es.URL)
	c.Scheme = u.Scheme
	c.Host = u.Host
	c.PathPrefix = u.Path

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://1/health", nil)

	client := &Client{name: "test", config: c, webClient: tu.NewTestWebClient()}
	client.HealthHandler(w, r)
	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}

}