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
	"io/ioutil"
	"testing"

	"github.com/Comcast/trickster/internal/proxy/request"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestProxyHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "test", nil, "influxdb", "/", "debug")
	rsc := request.GetResources(r)
	client.config = rsc.OriginConfig
	client.webClient = hc
	client.config.HTTPClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	client.ProxyHandler(w, r)
	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "test" {
		t.Errorf("expected 'test' got %s.", bodyBytes)
	}

}
