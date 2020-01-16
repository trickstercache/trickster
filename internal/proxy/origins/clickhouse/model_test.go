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

package clickhouse

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
)

func TestParts(t *testing.T) {

	rv1 := ResponseValue{
		"t":     "1557766080000",
		"cnt":   "27",
		"meta1": 200,
		"meta2": "value3",
	}

	metric, ts, val, _ := rv1.Parts("t", "cnt")

	expectedTs := time.Unix(1557766080, 0)
	expectedMetric := "{meta1=200;meta2=value3}"
	var expectedValue float64 = 27

	if ts != expectedTs {
		t.Errorf("expected %d got %d", expectedTs.Unix(), ts.Unix())
	}

	if metric != expectedMetric {
		t.Errorf("expected %s got %s", expectedMetric, metric)
	}

	if val != expectedValue {
		t.Errorf("expected %f got %f", expectedValue, val)
	}

	rv2 := ResponseValue{
		"t":   "1557766080000",
		"cnt": "27",
	}

	metric, _, _, _ = rv2.Parts("t", "cnt")
	if metric != "{}" {
		t.Errorf("expected '{}' got %s", metric)
	}

	rv3 := ResponseValue{
		"t":     "A557766080000",
		"cnt":   "27",
		"meta1": 200,
	}

	metric, _, _, _ = rv3.Parts("t", "cnt")
	if metric != "{}" {
		t.Errorf("expected '{}' got %s", metric)
	}

	rv4 := ResponseValue{
		"t":     "1557766080000",
		"cnt":   "2a7",
		"meta1": 200,
	}

	metric, _, _, _ = rv4.Parts("t", "cnt")
	if metric != "{}" {
		t.Errorf("expected '{}' got %s", metric)
	}

	rv5 := ResponseValue{
		"t":     "1557766080000",
		"cnt":   27.5,
		"meta1": 200,
	}

	metric, _, _, _ = rv5.Parts("t", "cnt")
	if metric != "{meta1=200}" {
		t.Errorf("expected '{meta1=200}' got %s", metric)
	}

}

var testJSON1 = []byte(`{"meta":[{"name":"t","type":"UInt64"},{"name":"cnt","type":"UInt64"},{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],"data":[{"t":"1557766080000","cnt":"12648509","meta1":200,"meta2":"value2"},{"t":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},{"t":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`)
var testJSON2 = []byte(`{"meta":[{"name":"t"}],"data":[{"t":"1557766080000","cnt":"12648509","meta1":200,"meta2":"value2"},{"t":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},{"t":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`) // should generate error

var testRE1 = &ResultsEnvelope{
	Meta: []FieldDefinition{
		{
			Name: "t",
			Type: "UInt64",
		},
		{
			Name: "cnt",
			Type: "UInt64",
		},
		{
			Name: "meta1",
			Type: "UInt16",
		},
		{
			Name: "meta2",
			Type: "String",
		},
	},

	SeriesOrder: []string{"1", "2", "3"},

	Data: map[string]*DataSet{
		"1": {
			Metric: map[string]interface{}{
				"meta1": 200,
				"meta2": "value2",
			},
			Points: []Point{
				{
					Timestamp: time.Unix(1557766080, 0),
					Value:     12648509,
				},
			},
		},
		"2": {
			Metric: map[string]interface{}{
				"meta1": 200,
				"meta2": "value3",
			},
			Points: []Point{
				{
					Timestamp: time.Unix(1557766080, 0),
					Value:     10260032,
				},
			},
		},
		"3": {
			Metric: map[string]interface{}{
				"meta1": 206,
				"meta2": "value3",
			},
			Points: []Point{
				{
					Timestamp: time.Unix(1557766080, 0),
					Value:     1,
				},
			},
		},
	},
}

func TestREMarshalJSON(t *testing.T) {

	expectedLen := len(testJSON1)

	re := ResultsEnvelope{}
	err := re.UnmarshalJSON(testJSON1)
	if err != nil {
		t.Error(err)
	}

	bytes, err := re.MarshalJSON()
	if err != nil {
		t.Error(err)
	}

	if len(bytes) != expectedLen {
		t.Errorf("expected %d got %d", expectedLen, len(bytes))
	}

	re.Meta = re.Meta[:0]
	_, err = re.MarshalJSON()
	if err == nil {
		t.Errorf("expected error: %s", `Must have at least two fields; only have 0`)
	}

}

func TestRSPMarshalJSON(t *testing.T) {

	rsp := &Response{ExtentList: timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(5, 0)}}}

	bytes, err := rsp.MarshalJSON()
	if err != nil {
		t.Error(err)
		return
	}

	rsp1 := &Response{}
	json.Unmarshal(bytes, rsp1)

	if rsp.ExtentList[0].Start != rsp1.ExtentList[0].Start {
		t.Errorf("expected %d got %d", rsp.ExtentList[0].Start.Unix(), rsp.ExtentList[0].Start.Unix())
	}

	if rsp.ExtentList[0].End != rsp1.ExtentList[0].End {
		t.Errorf("expected %d got %d", rsp.ExtentList[0].End.Unix(), rsp.ExtentList[0].End.Unix())
	}

}

func TestUnmarshalTimeseries(t *testing.T) {

	client := &Client{}
	ts, err := client.UnmarshalTimeseries(testJSON1)
	if err != nil {
		t.Error(err)
		return
	}

	re := ts.(*ResultsEnvelope)

	if len(re.Meta) != 4 {
		t.Errorf(`expected 4. got %d`, len(re.Meta))
		return
	}

	if len(re.Data) != 3 {
		t.Errorf(`expected 3. got %d`, len(re.Data))
		return
	}

	_, err = client.UnmarshalTimeseries(nil)
	if err == nil {
		t.Errorf("expected error: %s", `unexpected end of JSON input`)
		return
	}

	_, err = client.UnmarshalTimeseries(testJSON2)
	if err == nil {
		t.Errorf("expected error: %s", `Must have at least two fields; only have 1`)
		return
	}

}

func TestMarshalTimeseries(t *testing.T) {
	expectedLen := len(testJSON1)
	client := &Client{}
	bytes, err := client.MarshalTimeseries(testRE1)
	if err != nil {
		t.Error(err)
		return
	}
	if !reflect.DeepEqual(testJSON1, bytes) {
		t.Errorf("expected %d got %d", expectedLen, len(bytes))
	}
}

func TestUnmarshalJSON(t *testing.T) {

	re := ResultsEnvelope{}
	err := re.UnmarshalJSON(testJSON1)
	if err != nil {
		t.Error(err)
		return
	}

	if len(re.Meta) != 4 {
		t.Errorf(`expected 4. got %d`, len(re.Meta))
		return
	}

	m := re.Meta[2]
	if m.Name != "meta1" {
		t.Errorf(`expected meta1 found %s`, m.Name)
		return
	}

	if len(re.Data) != 3 {
		t.Errorf(`expected 3. got %d`, len(re.Data))
		return
	}

	key := "{meta1=206;meta2=value3}"
	v, ok := re.Data[key]
	if !ok {
		t.Errorf(`expected to find key %s`, key)
		return
	}

	if len(v.Points) != 1 {
		t.Errorf(`expected 1 got %d`, len(v.Points))
		return
	}

	if v.Points[0].Value != 1 {
		t.Errorf(`expected 1 got %f`, v.Points[0].Value)
		return
	}

	err = re.UnmarshalJSON(nil)
	if err == nil {
		t.Errorf("expected error: %s", `unexpected end of JSON input`)
		return
	}

	err = re.UnmarshalJSON(testJSON2)
	if err == nil {
		t.Errorf("expected error: %s", `Must have at least two fields; only have 1`)
		return
	}

}

func TestMSToTime(t *testing.T) {
	_, err := msToTime("bad")
	if err == nil {
		t.Errorf("expected error for invalid syntax")
	}
}

func TestSortPoints(t *testing.T) {

	p := Points{{Timestamp: time.Unix(1, 0), Value: 12}, {Timestamp: time.Unix(0, 0), Value: 13}, {Timestamp: time.Unix(2, 0), Value: 22}}
	sort.Sort(p)

	if p[0].Timestamp.Unix() != 0 {
		t.Errorf("expected %d got %d", 0, p[0].Timestamp.Unix())
	}

}
