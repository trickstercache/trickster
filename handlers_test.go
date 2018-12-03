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
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/go-kit/kit/log"
)

const (
	nonexistantOrigin       = "http://nonexistent-origin:54321"
	exampleQuery            = "/api/v1/query?query=up&time=2015-07-01T20:11:15.781Z"
	exampleRangeQuery       = "/api/v1/query_range?query=up&start=2015-07-01T20:10:30.781Z&end=2015-07-01T20:11:00.781Z&step=15"
	exampleRangeQuery_query = "up"
	exampleRangeQuery_start = "2015-07-01T20:10:30.781Z"
	exampleRangeQuery_end   = "2015-07-01T20:11:00.781Z"
	exampleRangeQuery_step  = "15"

	// this example should have 2 data points later than those in exampleRangeResponse
	exampleResponse = `{
   "status" : "success",
   "data" : {
      "resultType" : "vector",
      "result" : [
         {
            "metric" : {
               "__name__" : "up",
               "job" : "prometheus",
               "instance" : "localhost:9090"
            },
            "value": [ 1435781475.781, "1" ]
         },
         {
            "metric" : {
               "__name__" : "up",
               "job" : "node",
               "instance" : "localhost:9091"
            },
            "value" : [ 1435781475.781, "0" ]
         }
      ]
   }
}`

	// this example should have 6 data points
	// NOTE: Times in this response should end with '.781' not '.000'. Had
	//       to truncate due to how extents are measured in TricksterHandler.
	exampleRangeResponse = `{
   "status" : "success",
   "data" : {
      "resultType" : "matrix",
      "result" : [
         {
            "metric" : {
               "__name__" : "up",
               "job" : "prometheus",
               "instance" : "localhost:9090"
            },
            "values" : [
               [ 1435781430.000, "1" ],
               [ 1435781445.000, "1" ],
               [ 1435781460.000, "1" ]
            ]
         },
         {
            "metric" : {
               "__name__" : "up",
               "job" : "node",
               "instance" : "localhost:9091"
            },
            "values" : [
               [ 1435781430.000, "0" ],
               [ 1435781445.000, "0" ],
               [ 1435781460.000, "1" ]
            ]
         }
      ]
   }
}`
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
		OriginURL:           nonexistantOrigin,
		APIPath:             prometheusAPIv1Path,
		IgnoreNoCacheHeader: true,
		MaxValueAgeSecs:     86400,
	}
	tr = &TricksterHandler{
		ResponseChannels: make(map[string]chan *ClientRequestContext),
		Config:           conf,
		Logger:           log.NewNopLogger(),
		Metrics:          NewApplicationMetrics(),
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

func (t *TricksterHandler) setTestOrigin(originURL string) {
	conf := NewConfig()
	conf.Origins["default"] = PrometheusOriginConfig{
		OriginURL:           originURL,
		APIPath:             prometheusAPIv1Path,
		IgnoreNoCacheHeader: true,
		MaxValueAgeSecs:     86400,
	}
	t.Config = conf
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
			handler: (*TricksterHandler).promQueryHandler,
		},
		{
			handler: (*TricksterHandler).promQueryRangeHandler,
			path:    prometheusAPIv1Path + "query_range?start=0&end=100000000&step=15&query=up",
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
	paramsTests := []string{
		"start=0&end=100000000&query=up",
		"end=100000000&step=15&query=up",
		"start=0&step=15&query=up",
	}

	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)

	for _, params := range paramsTests {
		rr := httptest.NewRecorder()
		tr.promQueryRangeHandler(rr, httptest.NewRequest("GET", "http://trickster"+prometheusAPIv1Path+"query_range?"+params, nil))
		if rr.Result().StatusCode != http.StatusBadRequest {
			t.Errorf("unexpected status code for params %q; want %d, got %d", params, http.StatusBadRequest, rr.Result().StatusCode)
		}
	}
}

func newTestServer(body string) *httptest.Server {
	handler := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))
	return s
}

func TestTricksterHandler_pingHandler(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	es := newTestServer("{}")
	defer es.Close()
	tr.setTestOrigin(es.URL)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	tr.pingHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("wanted 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "pong" {
		t.Errorf("wanted 'pong' got %s.", bodyBytes)
	}

}

func TestTricksterHandler_getOrigin(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)

	// it should get test origin
	r := httptest.NewRequest("GET", nonexistantOrigin, nil)
	o := tr.getOrigin(r)
	if o.OriginURL != nonexistantOrigin {
		t.Errorf("wanted \"%s\" got \"%s\".", nonexistantOrigin, o.OriginURL)
	}
}

func TestTricksterHandler_promHealthCheckHandler(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	es := newTestServer("{}")
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should proxy request
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	tr.promHealthCheckHandler(w, r)

	if w.Result().StatusCode != 200 {
		t.Errorf("wanted 200 got %d.", w.Result().StatusCode)
	}
}

func TestTricksterHandler_promFullProxyHandler(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	es := newTestServer("{}")
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should proxy request
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	tr.promFullProxyHandler(w, r)

	if w.Result().StatusCode != 200 {
		t.Errorf("wanted 200 got %d.", w.Result().StatusCode)
	}
}

func TestTricksterHandler_promQueryHandler(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	es := newTestServer("{}")
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should proxy request
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)
	tr.promQueryHandler(w, r)

	if w.Result().StatusCode != 200 {
		t.Errorf("wanted 200 got %d.", w.Result().StatusCode)
	}
}

func TestTricksterHandler_promQueryRangeHandler_cacheMiss(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	es := newTestServer(exampleRangeResponse)
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should queue the proxy request
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL+exampleRangeQuery, nil)
	tr.promQueryRangeHandler(w, r)

	if w.Result().StatusCode != 200 {
		t.Errorf("wanted 200 got %d.", w.Result().StatusCode)
	}
}

func TestTricksterHandler_promQueryRangeHandler_cacheHit(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	es := newTestServer(exampleRangeResponse)
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// setup cache
	r := httptest.NewRequest("GET", es.URL+exampleRangeQuery, nil)
	tr.fetchPromQuery(es.URL+prometheusAPIv1Path+exampleRangeQuery_step, r.URL.Query(), r)

	// it should respond from cache
	w := httptest.NewRecorder()
	r = httptest.NewRequest("GET", es.URL+exampleRangeQuery, nil)
	tr.promQueryRangeHandler(w, r)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("wanted 200. got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	fmt.Println(string(bodyBytes))

	pm := PrometheusMatrixEnvelope{}
	err = json.Unmarshal(bodyBytes, &pm)
	if err != nil {
		t.Error(err)
	}

	if pm.getValueCount() != 6 {
		t.Errorf("wanted 6 got %d.", pm.getValueCount())
	}
}

func TestTricksterHandler_getURL(t *testing.T) {
	tr, closeFn := newTestTricksterHandler(t)
	defer closeFn(t)
	body := "{}"
	es := newTestServer(body)
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should get from the echo server
	b, _, _, err := tr.getURL("GET", es.URL, url.Values{}, nil)
	if err != nil {
		t.Error(err)
	}
	if bytes.Compare(b, []byte(body)) != 0 {
		t.Errorf("wanted \"%s\" got \"%s\"", body, b)
	}
}

func TestTricksterHandler_getVectorFromPrometheus(t *testing.T) {
	tr, closeTr := newTestTricksterHandler(t)
	defer closeTr(t)
	es := newTestServer(exampleResponse)
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should get an empty vector envelope
	r := httptest.NewRequest("GET", es.URL+exampleQuery, nil)
	pe, _, _, err := tr.getVectorFromPrometheus(es.URL, r.URL.Query(), r)
	if err != nil {
		t.Error(err)
	}
	if pe.Status != "success" {
		t.Errorf("wanted \"success\" got \"%s\".", pe.Status)
	}
}

func TestTricksterHandler_getMatrixFromPrometheus(t *testing.T) {
	tr, closeTr := newTestTricksterHandler(t)
	defer closeTr(t)
	es := newTestServer(exampleRangeResponse)
	defer es.Close()
	tr.setTestOrigin(es.URL)

	// it should get an empty matrix envelope
	r := httptest.NewRequest("GET", es.URL+exampleRangeQuery, nil)
	pe, _, _, _, err := tr.getMatrixFromPrometheus(es.URL, r.URL.Query(), r)
	if err != nil {
		t.Error(err)
	}
	if pe.Status != "success" {
		t.Errorf("wanted \"success\" got \"%s\".", pe.Status)
	}
}

func TestTricksterHandler_respondToCacheHit(t *testing.T) {
	tr, closeTr := newTestTricksterHandler(t)
	defer closeTr(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", nonexistantOrigin+exampleRangeQuery, nil)
	ctx, err := tr.buildRequestContext(w, r)
	if err != nil {
		t.Error(err)
	}

	// it should update the response in ctx.Writer without failing
	ctx.WaitGroup.Add(1)
	tr.respondToCacheHit(ctx)
}

func TestPrometheusMatrixEnvelope_getValueCount(t *testing.T) {
	pm := PrometheusMatrixEnvelope{}
	err := json.Unmarshal([]byte(exampleRangeResponse), &pm)
	if err != nil {
		t.Error(err)
	}

	// it should count the values in the matrix
	if 6 != pm.getValueCount() {
		t.Errorf("wanted 6 got %d.", pm.getValueCount())
	}
}

func TestTricksterHandler_mergeVector(t *testing.T) {
	tr, closeTr := newTestTricksterHandler(t)
	defer closeTr(t)

	pm := PrometheusMatrixEnvelope{}
	err := json.Unmarshal([]byte(exampleRangeResponse), &pm)
	if err != nil {
		t.Error(err)
	}

	pv := PrometheusVectorEnvelope{}
	err = json.Unmarshal([]byte(exampleResponse), &pv)
	if err != nil {
		t.Error(err)
	}

	// it should merge the values from the vector into the matrix
	pe := tr.mergeVector(pm, pv)

	if 8 != pe.getValueCount() {
		t.Errorf("wanted 8 got %d.", pe.getValueCount())
	}
}

func TestAlignStepBoundaries(t *testing.T) {
	tests := []struct {
		start, end, stepMS, now int64
		rangeStart, rangeEnd    int64
		err                     bool
	}{
		{
			1, 100, 10, 1000,
			0, 100,
			false,
		},

		{
			100, 1, 10, 1000,
			0, 0,
			true,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			s, e, err := alignStepBoundaries(test.start, test.end, test.stepMS, test.now)
			if hasErr := err != nil; hasErr != test.err {
				t.Fatalf("Mismatch in error: expected=%v actual=%v", test.err, hasErr)
			}
			if s != test.rangeStart {
				t.Fatalf("Mismatch in rangeStart: expected=%d actual=%d", test.rangeStart, s)
			}
			if e != test.rangeEnd {
				t.Fatalf("Mismatch in rangeStart: expected=%d actual=%d", test.rangeEnd, e)
			}
		})
	}
}
