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

package flux

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestMarshalTimeseriesCSVWriter(t *testing.T) {
	_, err := MarshalTimeseries(nil, nil, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Error("expected ErrUnknownFormat got", err)
	}
	b, err := MarshalTimeseries(testDataSet(), &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if len(b) == 0 {
		t.Error("expected non-nil response body")
	}
	if string(b) != testDataSetAsCSV {
		t.Error("unexpected CSV response\n" + string(b))
	}
	ts, err := UnmarshalTimeseries([]byte(testDataSetAsCSV), testTRQ)
	if err != nil {
		t.Error(err)
	}
	b, err = MarshalTimeseries(ts, &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if len(b) == 0 {
		t.Error("expected non-nil response body")
	}
	if string(b) != testDataSetAsCSV {
		t.Error("unexpected CSV response\n" + string(b))
	}
}
