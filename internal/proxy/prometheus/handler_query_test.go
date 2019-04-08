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
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestQueryHandler(t *testing.T) {

	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query_range?q=up&time=0", nil)

	client := &Client{Name: "default", Config: config.Origins["default"], Cache: cache}

	client.QueryHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("wanted 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("wanted '{}' got %s.", bodyBytes)
	}
}
