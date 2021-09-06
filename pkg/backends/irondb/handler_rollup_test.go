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

package irondb

import (
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestRollupHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		"{}", nil, "irondb", "/rollup/00112233-4455-6677-8899-aabbccddeeff/metric"+
			"?start_ts=0&end_ts=900&rollup_span=300s&type=average", "debug")
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

	client.RollupHandler(w, r)
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

func TestRollupHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
	o := bo.New()
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	o.Paths = client.DefaultPathConfigs(o)
	r, err := http.NewRequest(http.MethodGet, "http://0//rollup/00112233-4455-6677-8899-aabbccddeeff/metric", nil)
	if err != nil {
		t.Error(err)
	}

	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error")))

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	// should short circuit from internal checks
	// all though this func does not return a value to test, these exercise all coverage areas
	client.rollupHandlerSetExtent(nil, nil, nil)
	client.rollupHandlerSetExtent(r, nil, &timeseries.Extent{})
	client.rollupHandlerSetExtent(r, nil, &timeseries.Extent{Start: then, End: now})
	r.URL.RawQuery = "start_ts=0&end_ts=900&rollup_span=300s&type=average"
	client.rollupHandlerSetExtent(r, nil, &timeseries.Extent{Start: now, End: now})

}

func TestRollupHandlerParseTimeRangeQuery(t *testing.T) {

	// provide bad URL with no TimeRange query params
	// hc := tu.NewTestWebClient()
	o := bo.New()
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	o.Paths = client.DefaultPathConfigs(o)
	r, err := http.NewRequest(http.MethodGet, "http://0/rollup/00112233-4455-6677-8899-aabbccddeeff/metric", nil)
	if err != nil {
		t.Error(err)
	}

	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error")))

	// case where everything is good
	r.URL.RawQuery = "start_ts=0&end_ts=900&rollup_span=300s&type=average"
	trq, err := client.rollupHandlerParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}
	if trq == nil {
		t.Errorf("expected value got nil for %s", r.URL.RawQuery)
	}

	// missing start param
	r.URL.RawQuery = "end_ts=3456&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(r)
	expected := errors.MissingURLParam(upStart)
	if !reflect.DeepEqual(err, expected) {
		t.Errorf("expected %s got %s", expected.Error(), err)
	}

	// can't parse start param
	r.URL.RawQuery = "start_ts=abcd&end_ts=3456&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(r)
	expectedS := `unable to parse timestamp abcd: strconv.ParseInt: parsing "abcd": invalid syntax`
	if err.Error() != expectedS {
		t.Errorf("expected %s got %s", expectedS, err.Error())
	}

	// missing end param
	r.URL.RawQuery = "start_ts=9012&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(r)
	expected = errors.MissingURLParam(upEnd)
	if !reflect.DeepEqual(err, expected) {
		t.Errorf("expected %s got %s", expected.Error(), err)
	}

	// can't parse end param
	r.URL.RawQuery = "start_ts=9012&end_ts=efgh&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(r)
	expectedS = `unable to parse timestamp efgh: strconv.ParseInt: parsing "efgh": invalid syntax`
	if err.Error() != expectedS {
		t.Errorf("expected %s got %s", expectedS, err.Error())
	}

	// missing rollup_span param
	r.URL.RawQuery = "start_ts=9012&end_ts=3456"
	_, err = client.rollupHandlerParseTimeRangeQuery(r)
	expected = errors.MissingURLParam(upSpan)
	if !reflect.DeepEqual(err, expected) {
		t.Errorf("expected %s got %s", expected.Error(), err)
	}

	// unparsable rollup_span param
	r.URL.RawQuery = "start_ts=9012&end_ts=3456&rollup_span=pqrs"
	_, err = client.rollupHandlerParseTimeRangeQuery(r)
	expectedS = `unable to parse duration pqrs: time: invalid duration "pqrs"`
	if err.Error() != expectedS {
		t.Errorf("expected %s got %s", expectedS, err.Error())
	}

}

func TestRollupHandlerFastForwardRequestError(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, "irondb",
		"/rollup/00112233-4455-6677-8899-aabbccddeeff/metric", "debug")
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
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	_, err = client.rollupHandlerFastForwardRequest(r)
	if err == nil {
		t.Errorf("expected error: %s", "invalid parameters")
	}

}
