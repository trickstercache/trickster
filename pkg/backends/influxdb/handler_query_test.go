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

package influxdb

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

var testVals = url.Values(map[string][]string{"q": {
	`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h ` +
		`GROUP BY time(15s), "cluster" fill(null)`}, "epoch": {"ms"}})
var testRawQuery = testVals.Encode()

var testFluxVals = url.Values(map[string][]string{
	"q": {`from("test-bucket")
	|> range(start: -7d, stop: -6d)
	|> aggregateWindow(every: 1m, func: mean)
	`},
	"epoch": {"ms"},
})
var testFluxQuery = testFluxVals.Encode()

func TestParseTimeRangeQuery(t *testing.T) {

	req := &http.Request{
		Method: http.MethodGet,
		URL: &url.URL{
			Scheme:   "https",
			Host:     "blah.com",
			Path:     "/",
			RawQuery: testRawQuery,
		}}
	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if res.Step.Seconds() != 15 {
			t.Errorf("expected %d got %d", 15, int(res.Step.Seconds()))
		}
		if int(res.Extent.End.Sub(res.Extent.Start).Hours()) != 6 {
			t.Errorf("expected %d got %d", 6, int(res.Extent.End.Sub(res.Extent.Start).Hours()))
		}
	}

	body := testVals["q"][0]
	req, _ = http.NewRequest(http.MethodPost, "http://blah.com/", io.NopCloser(strings.NewReader(body)))
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))

	res, _, _, err = client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if res.Step.Seconds() != 15 {
			t.Errorf("expected %d got %d", 15, int(res.Step.Seconds()))
		}
		if int(res.Extent.End.Sub(res.Extent.Start).Hours()) != 6 {
			t.Errorf("expected %d got %d", 6, int(res.Extent.End.Sub(res.Extent.Start).Hours()))
		}
	}

	req = &http.Request{
		Method: http.MethodGet,
		URL: &url.URL{
			Scheme:   "https",
			Host:     "blah.com",
			Path:     "/",
			RawQuery: testFluxQuery,
		}}
	res, _, _, err = client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if int(res.Extent.End.Sub(res.Extent.Start).Hours()) != int(timeconv.Day.Hours()) {
			t.Errorf("expected %d got %d", int(timeconv.Day.Hours()), int(res.Extent.End.Sub(res.Extent.Start).Hours()))
		}
	}
}

func TestQueryHandlerWithSelect(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "{}",
		nil, "influxdb", "/query?q=select%20test", "debug")
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

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}

func TestQueryHandlerNotSelect(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "{}", nil, "influxdb", "/query", "debug")
	rsc := request.GetResources(r)

	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()
	client := backendClient.(*Client)
	rsc.BackendClient = client

	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

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
			"q_": {`SELECT mean("value") FROM "monthly"."rollup.1min" ` +
				`WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY time(15s), "cluster" fill(null)`},
			"epoch": {"ms"},
		}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryBadDuration(t *testing.T) {

	expected := errors.ErrStepParse

	req := &http.Request{
		Method: http.MethodGet,
		URL: &url.URL{
			Scheme: "https",
			Host:   "blah.com",
			Path:   "/",
			RawQuery: url.Values(map[string][]string{
				"q": {`SELECT mean("value") FROM "monthly"."rollup.1min" ` +
					`WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY times(15s), "cluster" fill(null)`},
				"epoch": {"ms"},
			}).Encode(),
		}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected.Error(), err.Error())
	}

}
