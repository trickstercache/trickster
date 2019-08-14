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
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/metrics"
	tu "github.com/Comcast/trickster/internal/util/testing"

	"github.com/influxdata/influxdb/pkg/testing/assert"
)

func init() {
	metrics.Init()
}

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: url.Values(map[string][]string{"q": {`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY time(15s), "cluster" fill(null)`}, "epoch": {"ms"}}).Encode(),
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err != nil {
		t.Error(err)
	} else {
		assert.Equal(t, int(res.Step.Seconds()), 15)
		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 6)
	}
}

func TestQueryHandlerWithSelect(t *testing.T) {

	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query?q=select%20test", nil)

	client := &Client{name: "default", config: config.Origins["default"], cache: cache, webClient: tu.NewTestWebClient()}

	client.QueryHandler(w, r)

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

func TestQueryHandlerNotSelect(t *testing.T) {

	es := tu.NewTestServer(200, "{}")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query", nil)

	client := &Client{name: "default", config: config.Origins["default"], webClient: tu.NewTestWebClient()}

	client.QueryHandler(w, r)

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

func TestParseTimeRangeQueryMissingQuery(t *testing.T) {
	expected := errors.MissingURLParam(upQuery).Error()
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"q_":    {`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY time(15s), "cluster" fill(null)`},
			"epoch": {"ms"},
		}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryBadDuration(t *testing.T) {

	expected := errors.StepParse().Error()

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"q":     {`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY times(15s), "cluster" fill(null)`},
			"epoch": {"ms"},
		}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

// func TestParseTimeRangeQueryWithBothTimes(t *testing.T) {
// 	req := &http.Request{URL: &url.URL{
// 		Scheme:   "https",
// 		Host:     "blah.com",
// 		Path:     "/",
// 		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h AND time < now() - 3h GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
// 	}}
// 	client := &Client{}
// 	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
// 	if err != nil {
// 		fmt.Println(err.Error())
// 	} else {
// 		assert.Equal(t, int(res.Step), 15)
// 		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 3)
// 	}
// }

// func TestParseTimeRangeQueryWithoutNow(t *testing.T) {
// 	req := &http.Request{URL: &url.URL{
// 		Scheme:   "https",
// 		Host:     "blah.com",
// 		Path:     "/",
// 		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time > 2052926911485ms AND time < 52926911486ms GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
// 	}}
// 	client := &Client{}
// 	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
// 	if err != nil {
// 		fmt.Println(err.Error())
// 	} else {
// 		assert.Equal(t, int(res.Step), 15)
// 		assert.Equal(t, res.Extent.End.UTC().Second()-res.Extent.Start.UTC().Second(), 1)
// 	}
// }

// func TestParseTimeRangeQueryWithAbsoluteTime(t *testing.T) {
// 	req := &http.Request{URL: &url.URL{
// 		Scheme:   "https",
// 		Host:     "blah.com",
// 		Path:     "/",
// 		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time < 2052926911486ms GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
// 	}}
// 	client := &Client{}
// 	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
// 	if err != nil {
// 		fmt.Println(err.Error())
// 	} else {
// 		assert.Equal(t, int(res.Step), 15)
// 		assert.Equal(t, res.Extent.Start.UTC().IsZero(), true)
// 	}
// }
