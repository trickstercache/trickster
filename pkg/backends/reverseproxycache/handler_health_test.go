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

package reverseproxycache

import (
	"io/ioutil"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	tu "github.com/tricksterproxy/trickster/pkg/util/testing"
)

func TestHealthHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, "rpc", "/health", "debug")
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil)
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	client := backendClient.(*Client)
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	rsc.BackendClient = client

	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}

	client.healthURL = &url.URL{}
	client.healthMethod = "-"
	client.HealthHandler(w, r)
	resp := w.Result()
	if resp.StatusCode != 400 {
		t.Errorf("Expected status: 400 got %d.", resp.StatusCode)
	}

	client.healthURL = nil
	client.HealthHandler(w, r)
	w = httptest.NewRecorder()
	resp = w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("Expected status: 200 got %d.", resp.StatusCode)
	}

}

func TestHealthHandlerCustomPath(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("../../../testdata/test.custom_health.conf",
		backendClient.DefaultPathConfigs, 200, "{}", nil, "rpc", "/health", "debug")
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

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
