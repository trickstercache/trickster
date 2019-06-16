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

package engines

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestDeriveCacheKey(t *testing.T) {

	client := &PromTestClient{
		config: &config.OriginConfig{
			PathsLookup: map[string]*config.ProxyPathConfig{
				"/": &config.ProxyPathConfig{
					CacheKeyParams:  []string{"query", "step", "time"},
					CacheKeyHeaders: []string{},
				},
			},
		},
	}

	u := &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r := &model.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}}
	key := DeriveCacheKey(client, client.config, r, "extra")

	if key != "6667a75e76dea9a5cd6c6ba73e5825b5" {
		t.Errorf("expected %s got %s", "6667a75e76dea9a5cd6c6ba73e5825b5", key)
	}

}

func TestQueryCache(t *testing.T) {

	expected := "1234"

	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	resp := &http.Response{}
	resp.Header = make(http.Header)
	resp.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp, []byte(expected))

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	d2, err := QueryCache(cache, "testKey")
	if err != nil {
		t.Error(err)
	}

	if string(d2.Body) != string(expected) {
		t.Errorf("expected %s got %s", string(expected), string(d2.Body))
	}

	if d2.StatusCode != 200 {
		t.Errorf("expected %d got %d", 200, d2.StatusCode)
	}

	_, err = QueryCache(cache, "testKey2")
	if err == nil {
		t.Errorf("expected error")
	}

}
