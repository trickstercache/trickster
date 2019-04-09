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
	"testing"

	"github.com/influxdata/influxdb/models"
)

func TestMarshalTimeseries(t *testing.T) {

	se := &SeriesEnvelope{
		Results: []Result{
			Result{
				Series: []models.Row{
					models.Row{
						Name:    "a",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName1": "tagValue1"},
						Values: [][]interface{}{
							[]interface{}{float64(1000), 1.5},
							[]interface{}{float64(5000), 1.5},
							[]interface{}{float64(10000), 1.5},
						},
					},
					models.Row{
						Name:    "b",
						Columns: []string{"time", "units"},
						Tags:    map[string]string{"tagName2": "tagValue2"},
						Values: [][]interface{}{
							[]interface{}{float64(1000), 2.5},
							[]interface{}{float64(5000), 2.1},
							[]interface{}{float64(10000), 2.4},
						},
					},
				},
			},
		},
	}

	expected := `{"results":[{"statement_id":0,"series":[{"name":"a","tags":{"tagName1":"tagValue1"},"columns":["time","units"],"values":[[1000,1.5],[5000,1.5],[10000,1.5]]},{"name":"b","tags":{"tagName2":"tagValue2"},"columns":["time","units"],"values":[[1000,2.5],[5000,2.1],[10000,2.4]]}]}]}`
	client := &Client{}
	bytes, err := client.MarshalTimeseries(se)
	if err != nil {
		t.Error(err)
		return
	}

	if string(bytes) != expected {
		t.Errorf("expected [%s] got [%s]", expected, string(bytes))
	}

}

func TestUnmarshalTimeseries(t *testing.T) {

	bytes := []byte(`{"results":[{"statement_id":0,"series":[{"name":"a","tags":{"tagName1":"tagValue1"},"columns":["time","units"],"values":[[1000,1.5],[5000,1.5],[10000,1.5]]},{"name":"b","tags":{"tagName2":"tagValue2"},"columns":["time","units"],"values":[[1000,2.5],[5000,2.1],[10000,2.4]]}]}]}`)
	client := &Client{}
	ts, err := client.UnmarshalTimeseries(bytes)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)

	if len(se.Results) != 1 {
		t.Errorf(`expected 1. got %d`, len(se.Results))
		return
	}

	if len(se.Results[0].Series) != 2 {
		t.Errorf(`expected 2. got %d`, len(se.Results[0].Series))
		return
	}

	if len(se.Results[0].Series[0].Values) != 3 {
		t.Errorf(`expected 3. got %d`, len(se.Results[0].Series[0].Values))
		return
	}

	if len(se.Results[0].Series[1].Values) != 3 {
		t.Errorf(`expected 3. got %d`, len(se.Results[0].Series[1].Values))
		return
	}

}
