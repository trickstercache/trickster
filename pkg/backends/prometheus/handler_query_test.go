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

package prometheus

import (
	"io"
	"net/http/httptest"
	"testing"

	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestQueryHandler(t *testing.T) {

	const expected = `{"status":"ok","data":{"resultType":"vector","result":[]}}`

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("",
		backendClient.DefaultPathConfigs, 200, expected, nil, "prometheus",
		"/api/v1/query?query=up&time=0", "debug")
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	rsc.BackendOptions.Prometheus = &po.Options{
		Labels: map[string]string{"test": "trickster"},
	}

	_, ok := rsc.BackendOptions.Paths[APIPath+mnQuery]
	if !ok {
		t.Errorf("could not find path config named %s", mnQuery)
	}

	client.hasTransformations = true
	client.QueryHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != expected {
		t.Errorf("expected '%s' got '%s'", expected, bodyBytes)
	}

	client.hasTransformations = false
	rsc.IsMergeMember = true

	w = httptest.NewRecorder()
	client.QueryHandler(w, r)

	resp = w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != expected {
		t.Errorf("expected '%s' got '%s'", expected, bodyBytes)
	}
}

func TestIndicateTransoformations(t *testing.T) {
	// passing test indicator is no panics
	indicateTransoformations(nil)
}
