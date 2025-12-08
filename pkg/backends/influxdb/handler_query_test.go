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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestQueryHandlerWithSelect(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "{}",
		nil, providers.InfluxDB, "/query?q=select%20test", "debug")
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

	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "{}", nil, providers.InfluxDB, "/query", "debug")
	require.NoError(t, err)
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
	expected := errors.ErrBadRequest
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
	if err != expected {
		t.Errorf(`Expected "%s", got "%s"`, expected, err)
	}
}

func TestParseTimeRangeQueryBadDuration(t *testing.T) {
	expected := pe.ErrStepParse

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
		},
	}
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
