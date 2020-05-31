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

package influxdb

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	tu "github.com/tricksterproxy/trickster/pkg/util/testing"

	"github.com/influxdata/influxdb/pkg/testing/assert"
)

var testVals = url.Values(map[string][]string{"q": {
	`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h ` +
		`GROUP BY time(15s), "cluster" fill(null)`}, "epoch": {"ms"}})
var testRawQuery = testVals.Encode()

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
	res, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		assert.Equal(t, int(res.Step.Seconds()), 15)
		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 6)
	}

	req, _ = http.NewRequest(http.MethodPost, "http://blah.com/",
		io.Reader(bytes.NewBufferString(testRawQuery)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", strconv.Itoa(len(testRawQuery)))

	res, err = client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		assert.Equal(t, int(res.Step.Seconds()), 15)
		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 6)
	}
}

func TestQueryHandlerWithSelect(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("",
		client.DefaultPathConfigs, 200, "{}", nil, "influxdb", "/query?q=select%20test", "debug")
	rsc := request.GetResources(r)
	rsc.OriginClient = client
	client.config = rsc.OriginConfig
	client.webClient = hc
	client.config.HTTPClient = hc
	client.baseUpstreamURL, _ = url.Parse(ts.URL)
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

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}

func TestQueryHandlerNotSelect(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "influxdb", "/query", "debug")
	rsc := request.GetResources(r)
	client.config = rsc.OriginConfig
	client.webClient = hc
	client.config.HTTPClient = hc
	client.baseUpstreamURL, _ = url.Parse(ts.URL)
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
			"q_": {`SELECT mean("value") FROM "monthly"."rollup.1min" ` +
				`WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY time(15s), "cluster" fill(null)`},
			"epoch": {"ms"},
		}).Encode(),
	}}
	client := &Client{}
	_, err := client.ParseTimeRangeQuery(req)
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
	_, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`Expected "%s", got NO ERROR`, expected)
		return
	}
	if err != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected.Error(), err.Error())
	}

}
