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

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTime(t *testing.T) {
	fixtures := []struct {
		input  string
		output string
	}{
		{"2018-04-07T05:08:53.200Z", "2018-04-07 05:08:53.2 +0000 UTC"},
		{"1523077733", "2018-04-07 05:08:53 +0000 UTC"},
		{"1523077733.2", "2018-04-07 05:08:53.200000047 +0000 UTC"},
	}

	for _, f := range fixtures {
		out, err := parseTime(f.input)
		if err != nil {
			t.Error(err)
		}

		outStr := out.UTC().String()
		if outStr != f.output {
			t.Errorf("Expected %s, got %s for input %s", f.output, outStr, f.input)
		}
	}
}

func newTestTricksterHandler(t *testing.T) (tr *TricksterHandler, close func(t *testing.T)) {
	conf := NewConfig()
	conf.Origins["default"] = PrometheusOriginConfig{
		OriginURL:           "http://nonexistent-origin:54321/",
		APIPath:             prometheusAPIv1Path,
		DefaultStep:         300,
		IgnoreNoCacheHeader: true,
		MaxValueAgeSecs:     86400,
	}
	logger := newLogger(conf.Logging, "")
	tr = &TricksterHandler{
		ResponseChannels: make(map[string]chan *ClientRequestContext),
		Config:           conf,
		Logger:           logger,
		Metrics:          NewApplicationMetrics(conf, logger),
	}

	tr.Cacher = getCache(tr)
	if err := tr.Cacher.Connect(); err != nil {
		t.Fatal("Unable to connect to cache:", err)
	}

	return tr, func(t *testing.T) {
		tr.Metrics.Unregister()
		if err := tr.Cacher.Close(); err != nil {
			t.Fatal("Error closing cacher:", err)
		}
	}
}

func TestUnreachableOriginReturnsStatusBadGateway(t *testing.T) {
	tests := []struct {
		handler func(*TricksterHandler, http.ResponseWriter, *http.Request)
		path    string
	}{
		{
			handler: (*TricksterHandler).promHealthCheckHandler,
		},
		{
			handler: (*TricksterHandler).promFullProxyHandler,
		},
		{
			handler: (*TricksterHandler).promAPIProxyHandler,
		},
		{
			handler: (*TricksterHandler).promQueryHandler,
		},
		{
			handler: (*TricksterHandler).promQueryRangeHandler,
			path:    prometheusAPIv1Path + "query_range?start=0&end=100000000&query=up",
		},
	}

	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)

	for _, test := range tests {
		rr := httptest.NewRecorder()
		test.handler(tr, rr, httptest.NewRequest("GET", "http://trickster"+test.path, nil))
		if rr.Result().StatusCode != http.StatusBadGateway {
			t.Errorf("unexpected status code; want %d, got %d", http.StatusBadGateway, rr.Result().StatusCode)
		}
	}
}

func TestMissingRangeQueryParametersResultInStatusBadRequest(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)

	rr := httptest.NewRecorder()
	tr.promQueryRangeHandler(rr, httptest.NewRequest("GET", "http://trickster"+prometheusAPIv1Path+"query_range", nil))
	if rr.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("unexpected status code; want %d, got %d", http.StatusBadRequest, rr.Result().StatusCode)
	}
}
