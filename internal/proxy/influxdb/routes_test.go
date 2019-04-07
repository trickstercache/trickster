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

package influxdb

import (
	"net/http/httptest"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	tu "github.com/Comcast/trickster/internal/util/testing"

	"github.com/gorilla/mux"
)

func TestRegisterRoutesNoDefault(t *testing.T) {

	routing.Router = mux.NewRouter()

	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := config.Origins["default"]
	client := Client{Config: oc}
	client.RegisterRoutes("test_default", oc)

	// This should be false
	r := httptest.NewRequest("GET", "http://0/health", nil)
	rm := &mux.RouteMatch{}
	if routing.Router.Match(r, rm) {
		t.Errorf("unexpected route match")
		return
	}

	// This should be true
	r = httptest.NewRequest("GET", "http://0/test_default/health", nil)
	if !routing.Router.Match(r, rm) {
		t.Errorf("could not match route")
		return
	}

}

func TestRegisterRoutesDefault(t *testing.T) {

	routing.Router = mux.NewRouter()
	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := config.Origins["default"]
	client := Client{Config: oc}
	client.RegisterRoutes("default", oc)

	// This should be false
	r := httptest.NewRequest("GET", "http://0/health", nil)
	rm := &mux.RouteMatch{}
	if !routing.Router.Match(r, rm) {
		t.Errorf("could not match route")
		return
	}

	// This should be true
	r = httptest.NewRequest("GET", "http://0/default/health", nil)
	if !routing.Router.Match(r, rm) {
		t.Errorf("could not match route")
		return
	}

}
