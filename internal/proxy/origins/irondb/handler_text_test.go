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

package irondb

import (
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestTextHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/read/0/900/00112233-4455-6677-8899-aabbccddeeff/metric", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	client.TextHandler(w, r)
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

func TestTextHandlerDeriveCacheKey(t *testing.T) {

	client := &Client{name: "test"}
	path := "/read/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, _, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", path, "debug")
	if err != nil {
		t.Error(err)
	}

	const expected = "a506d1700414b1d0ac15340bd619fdab"
	result := client.textHandlerDeriveCacheKey(path, r.URL.Query(), r.Header, r.Body, "extra")
	if result != expected {
		t.Errorf("expected %s got %s", expected, result)
	}

}

func TestTextHandlerParseTimeRangeQuery(t *testing.T) {

	path := "/read/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	r, err := http.NewRequest(http.MethodGet, "http://0"+path, nil)
	if err != nil {
		t.Error(err)
	}

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)

	tr := model.NewRequest("RollupHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	// case where everything is good
	_, err = client.textHandlerParseTimeRangeQuery(tr)
	if err != nil {
		t.Error(err)
	}

	// case where the path is not long enough
	r.URL.Path = "/read/0/900/"
	expected := errors.NotTimeRangeQuery().Error()
	_, err = client.textHandlerParseTimeRangeQuery(tr)
	if err == nil || err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	// case where the start can't be parsed
	r.URL.Path = "/read/z/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	expected = `unable to parse timestamp z: strconv.ParseInt: parsing "z": invalid syntax`
	_, err = client.textHandlerParseTimeRangeQuery(tr)
	if err == nil || err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	// case where the end can't be parsed
	r.URL.Path = "/read/0/z/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, err = client.textHandlerParseTimeRangeQuery(tr)
	if err == nil || err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

}

func TestTextHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/test", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("TextHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	client.textHandlerSetExtent(tr, &timeseries.Extent{Start: then, End: now})
	if r.URL.Path != "/test" {
		t.Errorf("expected %s got %s", "/test", r.URL.Path)
	}

}
