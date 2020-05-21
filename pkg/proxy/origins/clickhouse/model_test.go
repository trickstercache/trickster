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
	"reflect"
	"testing"
	"time"
)

var testJSON1 = []byte(`{"meta":[{"name":"t","type":"UInt64"},{"name":"cnt","type":"UInt64"},` +
	`{"name":"meta1","type":"UInt16"},{"name":"meta2","type":"String"}],` +
	`"data":[{"t":"1557766080000","cnt":"12648509","meta1":200,"meta2":"value2"},` +
	`{"t":"1557766680000","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"t":"1557767280000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`,
)
var testJSON2 = []byte(`{"meta":[{"name":"t"}],"data":[{"t":"1557766080000","cnt":"12648509",` +
	`"meta1":200,"meta2":"value2"},{"t":"1557766080000","cnt":"10260032","meta1":200,"meta2":"value3"},` +
	`{"t":"1557766080000","cnt":"1","meta1":206,"meta2":"value3"}],"rows":3}`,
) // should generate error

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

	Data: []Point{
		{
			Timestamp: time.Unix(1557766080, 0),
			Values: map[string]interface{}{
				"cnt":   12648509,
				"meta1": 200,
				"meta2": "value2",
			},
		},
		{
			Timestamp: time.Unix(1557766680, 0),
			Values: map[string]interface{}{
				"cnt":   10260032,
				"meta1": 200,
				"meta2": "value3",
			},
		},
		{Timestamp: time.Unix(1557767280, 0),
			Values: map[string]interface{}{
				"cnt":   1,
				"meta1": 206,
				"meta2": "value3",
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

	if re.Data[0].Values["cnt"] != 1 {
		t.Errorf(`expected 1 got %f`, re.Data[0].Values["cnt"])
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
