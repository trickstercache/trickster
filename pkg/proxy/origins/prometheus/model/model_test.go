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

package model

import (
	"testing"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"github.com/tricksterproxy/trickster/pkg/timeseries/dataset"
)

const testDoc = `{"status":"success","data":{"resultType":"matrix","result":[{` +
	`"metric":{"__name__":"up","instance":"localhost:9090","job":"prometheus"},"values"` +
	`:[[1435781430,"1"],[1435781445,"1"],[1435781460,"1"]]},{"metric":` +
	`{"__name__":"up","instance":"localhost:9091","job":"node"},"values":` +
	`[[1435781430,"0"],[1435781445,"0"],[1435781460,"1"]]}]}}`

func TestUnmarshalTimeseries(t *testing.T) {
	b := []byte(testDoc)
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries(b, trq)
	if err != nil {
		t.Error(err)
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Error(timeseries.ErrUnknownFormat)
	}

	b, err = MarshalTimeseries(ds, nil)
	if err != nil {
		t.Error(err)
	}

	if string(b) != testDoc {
		t.Error("marsahing error")
	}

}

func TestUnmarshalInstantaneous(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	bytes := []byte(`{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up","instance":"localhost:9090","job":"prometheus"},` +
		`"value":[1554730772.113,"1"]}]}}`)
	_, err := UnmarshalTimeseries(bytes, trq)
	if err != nil {
		t.Error(err)
		return
	}
}
