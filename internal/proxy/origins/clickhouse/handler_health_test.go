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

package clickhouse

import (
	"io/ioutil"
	"net/http/httptest"
	"testing"

	"github.com/Comcast/trickster/internal/proxy/request"
	"github.com/Comcast/trickster/internal/util/metrics"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func init() {
	metrics.Init()
}

func TestHealthHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "clickhouse", "/health", "debug")

	rsc := request.GetResources(r)
	client.config = rsc.OriginConfig
	client.webClient = hc
	client.config.HTTPClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

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

	client.healthMethod = "-"

	w = httptest.NewRecorder()
	client.HealthHandler(w, r)
	resp = w.Result()
	if resp.StatusCode != 400 {
		t.Errorf("Expected status: 400 got %d.", resp.StatusCode)
	}

}

func TestHealthHandlerCustomPath(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("../../../../testdata/test.custom_health.conf", client.DefaultPathConfigs, 200, "{}", nil, "clickhouse", "/health", "debug")
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	rsc := request.GetResources(r)
	client.config = rsc.OriginConfig

	client.webClient = hc
	client.config.HTTPClient = hc

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
