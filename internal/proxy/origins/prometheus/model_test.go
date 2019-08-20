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

package prometheus

import (
	"testing"

	"github.com/prometheus/common/model"
)

func TestMarshalTimeseries(t *testing.T) {

	me := &MatrixEnvelope{
		Data: MatrixData{
			ResultType: "matrix",
			Result: model.Matrix{
				&model.SampleStream{
					Metric: model.Metric{"__name__": "a"},
					Values: []model.SamplePair{
						{Timestamp: 99000, Value: 1.5},
						{Timestamp: 199000, Value: 1.5},
						{Timestamp: 299000, Value: 1.5},
					},
				},
				&model.SampleStream{
					Metric: model.Metric{"__name__": "b"},
					Values: []model.SamplePair{
						{Timestamp: 99000, Value: 1.5},
						{Timestamp: 199000, Value: 1.5},
						{Timestamp: 299000, Value: 1.5},
					},
				},
			},
		},
	}

	expected := `{"status":"","data":{"resultType":"matrix","result":[{"metric":{"__name__":"a"},"values":[[99,"1.5"],[199,"1.5"],[299,"1.5"]]},{"metric":{"__name__":"b"},"values":[[99,"1.5"],[199,"1.5"],[299,"1.5"]]}]}}`
	client := &Client{logger: logger}
	bytes, err := client.MarshalTimeseries(me)
	if err != nil {
		t.Error(err)
		return
	}

	if string(bytes) != expected {
		t.Errorf("expected [%s] got [%s]", expected, string(bytes))
	}

}

func TestUnmarshalTimeseries(t *testing.T) {

	bytes := []byte(`{"status":"","data":{"resultType":"matrix","result":[{"metric":{"__name__":"a"},"values":[[99,"1.5"],[199,"1.5"],[299,"1.5"]]},{"metric":{"__name__":"b"},"values":[[99,"1.5"],[199,"1.5"],[299,"1.5"]]}]}}`)
	client := &Client{logger: logger}
	ts, err := client.UnmarshalTimeseries(bytes)
	if err != nil {
		t.Error(err)
		return
	}

	me := ts.(*MatrixEnvelope)

	if len(me.Data.Result) != 2 {
		t.Errorf(`expected 2. got %d`, len(me.Data.Result))
		return
	}

	if len(me.Data.Result[0].Values) != 3 {
		t.Errorf(`expected 3. got %d`, len(me.Data.Result[0].Values))
		return
	}

	if len(me.Data.Result[1].Values) != 3 {
		t.Errorf(`expected 3. got %d`, len(me.Data.Result[1].Values))
		return
	}

}

func TestUnmarshalInstantaneous(t *testing.T) {

	bytes := []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up","instance":"localhost:9090","job":"prometheus"},"value":[1554730772.113,"1"]}]}}`)
	client := &Client{logger: logger}
	ts, err := client.UnmarshalInstantaneous(bytes)
	if err != nil {
		t.Error(err)
		return
	}

	me := ts.(*MatrixEnvelope)

	if len(me.Data.Result) != 1 {
		t.Errorf(`expected 1. got %d`, len(me.Data.Result))
		return
	}

	if len(me.Data.Result[0].Values) != 1 {
		t.Errorf(`expected 3. got %d`, len(me.Data.Result[0].Values))
		return
	}

	if me.Data.Result[0].Values[0].Value != 1 {
		t.Errorf(`expected 1. got %d`, len(me.Data.Result[0].Values))
		return
	}

}

func TestUnmarshalInstantaneousFails(t *testing.T) {

	bytes := []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up","instance":"localhost:9090","job":"prometheus"},"value":[1554730772.113,"1"]}]}`)
	client := &Client{logger: logger}
	_, err := client.UnmarshalInstantaneous(bytes)
	if err == nil {
		t.Errorf("expected error: 'unexpected end of JSON input'")
		return
	}

}
