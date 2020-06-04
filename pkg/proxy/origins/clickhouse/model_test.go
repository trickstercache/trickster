/*
 * Copyright 2020 Comcast Cable Communications Management, LLC
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

package clickhouse

import (
	tt "github.com/tricksterproxy/trickster/pkg/proxy/timeconv"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"reflect"
	"testing"
	"time"
)

func newRe() *ResultsEnvelope {
	return &ResultsEnvelope{ExtentList: timeseries.ExtentList{}, Data: make([]Point, 0)}
}

func (re *ResultsEnvelope) addMeta(values ...string) *ResultsEnvelope {
	for i := 0; i < len(values); i += 2 {
		re.Meta = append(re.Meta, FieldDefinition{Name: values[i], Type: values[i+1]})
	}
	return re
}

func (re *ResultsEnvelope) addPoint(ts int, values ...interface{}) *ResultsEnvelope {
	v := ResponseValue{}
	for i := 0; i < len(re.Meta)-1; i++ {
		v[re.Meta[i+1].Name] = values[i]
	}
	re.Data = append(re.Data, Point{Timestamp: time.Unix(int64(ts), 0), Values: []ResponseValue{v}})
	return re
}

func (re *ResultsEnvelope) addExtent(e int64) *ResultsEnvelope {
	re.ExtentList = append(re.ExtentList, timeseries.Extent{Start: time.Unix(e, 0), End: time.Unix(e, 0)})
	return re
}

func (re *ResultsEnvelope) addExtents(e ...int64) *ResultsEnvelope {
	for i := 0; i < len(e); i += 2 {
		re.ExtentList = append(re.ExtentList, timeseries.Extent{Start: time.Unix(e[i], 0), End: time.Unix(e[i+1], 0)})
	}
	return re
}

func (re *ResultsEnvelope) setStep(s string) *ResultsEnvelope {
	re.StepDuration, _ = tt.ParseDuration(s)
	return re
}

var testJSON1 = `{"meta":[{"name":"t","type":"UInt64"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"cnt":"12648509","meta1":200,"meta2":"value2","t":"1557766080000"},` +
	`{"cnt":"10260032","meta1":200,"meta2":"value3","t":"1557766680000"},` +
	`{"cnt":"1","meta1":206,"meta2":"value3","t":"1557767280000"}],"rows":3}`

var testJSONStringDate = `{"meta":[{"name":"t","type":"DateTime('Etc/UTC')"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"cnt":"12648509","meta1":200,"meta2":"value2","t":"2019-05-13 10:48:00"},` +
	`{"cnt":"10260032","meta1":200,"meta2":"value3","t":"2019-05-13 10:58:00"},` +
	`{"cnt":"1","meta1":206,"meta2":"value3","t":"2019-05-13 11:08:00"}],"rows":3}`

var testJSONUInt32Date = `{"meta":[{"name":"t","type":"UInt32"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"cnt":"12648509","meta1":200,"meta2":"value2","t":1557766080},` +
	`{"cnt":"10260032","meta1":200,"meta2":"value3","t":1557766680},` +
	`{"cnt":"1","meta1":206,"meta2":"value3","t":1557767280}],"rows":3}`

var testEmptyJSON = `{"meta":[{"name":"t","type":"UInt64"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[],"rows": 0}`

var testJSONInt32 = `{"meta":[{"name":"t","type":"UInt32"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"cnt":"12648509","meta1":200,"meta2":"value2","t":1557766080},` +
	`{"cnt":"10260032","meta1":200,"meta2":"value3","t":1557766680},` +
	`{"cnt":"1","meta1":206,"meta2":"value3","t":1557767280}],"rows":3}`

var testJSONInt64 = `{"meta":[{"name":"t","type":"UInt64"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"cnt":"12648509","meta1":200,"meta2":"value2","t":1557766080000},` +
	`{"cnt":"10260032","meta1":200,"meta2":"value3","t":1557766680000},` +
	`{"cnt":"1","meta1":206,"meta2":"value3","t":1557767280000}],"rows":3}`

var testJSONDateTime = `{"meta":[{"name":"t","type":"DateTime('Etc/UTC')"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"cnt":"12648509","meta1":200,"meta2":"value2","t":"2019-05-13 16:48:00"},` +
	`{"cnt":"10260032","meta1":200,"meta2":"value3","t":"2019-05-13 16:58:00"},` +
	`{"cnt":"1","meta1":206,"meta2":"value3","t":"2019-05-13 17:08:00"}],"rows":3}`

var testRE = newRe().addMeta("t", "UInt64", "cnt", "UInt64", "meta1", "UInt16", "meta2", "String").
	addPoint(1557766080, "12648509", 200, "value2").
	addPoint(1557766680, "10260032", 200, "value3").
	addPoint(1557767280, "1", 206, "value3")

var testREStringDate = newRe().addMeta("t", "DateTime('Etc/UTC')", "cnt", "UInt64", "meta1", "UInt16", "meta2", "String").
	addPoint(1557766080, "12648509", 200, "value2").
	addPoint(1557766680, "10260032", 200, "value3").
	addPoint(1557767280, "1", 206, "value3")

var testREUInt32Date = newRe().addMeta("t", "UInt32", "cnt", "UInt64", "meta1", "UInt16", "meta2", "String").
	addPoint(1557766080, "12648509", 200, "value2").
	addPoint(1557766680, "10260032", 200, "value3").
	addPoint(1557767280, "1", 206, "value3")

func TestREMarshalJSON(t *testing.T) {
	expectedLen := len(testJSON1)

	re := ResultsEnvelope{}
	err := re.UnmarshalJSON([]byte(testJSON1))
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
		t.Errorf("expected error: %s", `no Meta in ResultsEnvelope`)
	}

}

func TestUnmarshalTimeseries(t *testing.T) {
	client := &Client{}
	ts, err := client.UnmarshalTimeseries([]byte(testJSON1))
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

}

func TestMarshalTimeseries(t *testing.T) {
	expectedLen := len(testJSON1)
	client := &Client{}
	bytes, err := client.MarshalTimeseries(testRE)
	if err != nil {
		t.Error(err)
		return
	}
	if !reflect.DeepEqual([]byte(testJSON1), bytes) {
		t.Errorf("expected %d got %d", expectedLen, len(bytes))
	}

	bytes, err = client.MarshalTimeseries(testREStringDate)
	if err != nil {
		t.Error(err)
		return
	}

	if string(bytes) != testJSONStringDate {
		t.Errorf("Expected %s got %s", testJSONStringDate, string(bytes))
	}

	bytes, err = client.MarshalTimeseries(testREUInt32Date)
	if err != nil {
		t.Error(err)
		return
	}

	if !reflect.DeepEqual([]byte(testJSONUInt32Date), bytes) {
		t.Errorf("got %s", string(bytes))
	}
}

func TestUnmarshalValidJSON(t *testing.T) {
	re := ResultsEnvelope{}
	err := re.UnmarshalJSON([]byte(testJSON1))
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
	if re.Data[0].Values[0]["cnt"] != "12648509" {
		t.Errorf(`expected 1 got %s`, re.Data[0].Values[0]["cnt"])
		return
	}

	err = re.UnmarshalJSON([]byte(testJSONInt32))
	if err != nil {
		t.Error(err)
		return
	}
	if re.Data[0].Timestamp != time.Unix(1557766080, 0) {
		t.Errorf("incorrect timestamp for point %v", re.Data[0])
		return
	}

	err = re.UnmarshalJSON([]byte(testJSONInt64))
	if err != nil {
		t.Error(err)
		return
	}
	if re.Data[1].Timestamp.Unix() != time.Unix(1557766680, 0).Unix() {
		t.Errorf("incorrect timestamp for point %v", re.Data[1])
		return
	}

	re = ResultsEnvelope{}
	err = re.UnmarshalJSON([]byte(testJSONDateTime))
	if err != nil {
		t.Error(err)
	}

	if re.Data[1].Timestamp.Unix() != time.Unix(1557766680, 0).Unix() {
		t.Errorf("incorrect timestamp for point %v", re.Data[1])
		return
	}

	err = re.UnmarshalJSON([]byte(testEmptyJSON))
	if err != nil {
		t.Error(err)
		return
	}
}

var testUnknownTimestampJSON = `{"meta":[{"name":"t", "type":"String"},{"name":"cnt", "type":"UInt64"}],` +
	`"data":[{"t":"1557766080000","cnt":"12648509","meta1":200,"meta2":"value2"},` +
	`{"t":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"t":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`

var testUnexpectedTimestamp = `{"meta":[{"name":"t", "type":"UInt64"},{"name":"cnt", "type":"UInt64"}],` +
	`"data":[{"t":"15577660800000","cnt":"12648509","meta1":200,"meta2":"value2"},` +
	`{"t":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"t":true,"cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`

var testMissingTimestampJSON = `{"meta":[{"name":"t", "type":"UInt64"},{"name":"cnt", "type":"UInt64"}],` +
	`"data":[{"t":"1557766080000","cnt":"12648509","meta1":200,"meta2":"value2"},` +
	`{"bad":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"bad":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`

var testNullTimestampJSON = `{"meta":[{"name":"t", "type":"UInt32"},{"name":"cnt", "type":"UInt64"}],` +
	`"data":[{"t":null,"cnt":"12648509","meta1":200,"meta2":"value2"},` +
	`{"t":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"t":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`

var testInvalidTimestampJSON = `{"meta":[{"name":"t","type":"UInt64"},{"name":"cnt", "type":"UInt64"}],` +
	`"data":[{"t":"1557766080000","cnt":"12648509","meta1":200,"meta2":"value2"},` +
	`{"t":"1557766080000bad","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"t":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`

func TestUnmarshallBadJSON(t *testing.T) {
	test := func(run string, json string, error string) {
		t.Run(run, func(t *testing.T) {
			re := &ResultsEnvelope{}
			err := re.UnmarshalJSON([]byte(json))
			if err == nil {
				t.Errorf("Expected error parsing JSON")
			} else if err.Error() != error {
				t.Errorf("expected error: %s, got: %s", error, err.Error())
			}
		})
	}
	test("no json data", "", "unexpected end of JSON input")
	test("bad timestamp type", testUnknownTimestampJSON, "timestamp field not of recognized type")
	test("unexpected timestamp field type", testUnexpectedTimestamp, "timestamp field does not parse to date")
	test("null timestamp field", testNullTimestampJSON, "timestamp field does not parse to date")
	test("invalid timestamp field", testInvalidTimestampJSON, "timestamp field does not parse to date")
	test("missing timestamp field", testMissingTimestampJSON, "missing timestamp field in response data")
}
