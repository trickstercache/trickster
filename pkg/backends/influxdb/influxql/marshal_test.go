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

package influxql

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestMarshalTimeseries(t *testing.T) {

	_, err := MarshalTimeseries(nil, nil, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Error("expected ErrUnknownFormat got", err)
	}

	trq := &timeseries.TimeRangeQuery{
		Statement: "hello",
	}

	ts, err := UnmarshalTimeseries([]byte(testDoc01), trq)
	if err != nil {
		t.Error(err)
	}

	rlo := &timeseries.RequestOptions{}
	w := httptest.NewRecorder()
	err = MarshalTimeseriesWriter(ts, rlo, 200, w)
	if err != nil {
		t.Error(err)
	}

	if !strings.HasPrefix(w.Header().Get(headers.NameContentType), headers.ValueApplicationJSON) {
		t.Error("expected JSON content type header; got", w.Header().Get(headers.NameContentType))
	}

	rlo.OutputFormat = 5

	_, err = MarshalTimeseries(ts, rlo, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Error("expected ErrUnknownFormat got", err)
	}

	rlo.OutputFormat = 1
	w = httptest.NewRecorder()
	err = MarshalTimeseriesWriter(ts, rlo, 200, w)
	if err != nil {
		t.Error(err)
	}
	if !strings.HasPrefix(w.Header().Get(headers.NameContentType), headers.ValueApplicationJSON) {
		t.Error("expected JSON content type header; got", w.Header().Get(headers.NameContentType))
	}

}
