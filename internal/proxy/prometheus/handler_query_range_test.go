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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	tu "github.com/Comcast/trickster/internal/util/testing"

	"github.com/influxdata/influxdb/pkg/testing/assert"
)

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": []string{`up`},
			"start": []string{strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   []string{strconv.Itoa(int(time.Now().Unix()))},
			"step":  []string{"15"},
		}).Encode(),
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL})
	if err != nil {
		t.Error(err)
	} else {
		assert.Equal(t, int(res.Step.Seconds()), 15)
		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 6)
	}
}

func TestParseTimeRangeQueryMissingQuery(t *testing.T) {
	expected := proxy.ErrorMissingURLParam(upQuery).Error()
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query_": []string{`up`},
			"start":  []string{strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":    []string{strconv.Itoa(int(time.Now().Unix()))},
			"step":   []string{"15"}}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryBadDuration(t *testing.T) {

	expected := `Unable to parse duration: x`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": []string{`up`},
			"start": []string{strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   []string{strconv.Itoa(int(time.Now().Unix()))},
			"step":  []string{"x"}}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryNoStart(t *testing.T) {

	expected := `missing URL parameter: [start]`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": []string{`up`},
			"end":   []string{strconv.Itoa(int(time.Now().Unix()))},
			"step":  []string{"x"}}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryNoEnd(t *testing.T) {

	expected := `missing URL parameter: [end]`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": []string{`up`},
			"start": []string{strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"step":  []string{"x"}}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryNoStep(t *testing.T) {

	expected := `missing URL parameter: [step]`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": []string{`up`},
			"start": []string{strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   []string{strconv.Itoa(int(time.Now().Unix()))}},
		).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestQueryRangeHandler(t *testing.T) {

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
	r := httptest.NewRequest("GET", "http://0/query_range?q=up&start=0&end=900&step=15", nil)

	client := &Client{Name: "default", Config: config.Origins["default"], Cache: cache}

	client.QueryRangeHandler(w, r)

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
