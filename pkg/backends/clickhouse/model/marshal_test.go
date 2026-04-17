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

package model

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func FuzzWFDataItemMarshalJSON(f *testing.F) {
	f.Add("key1", "value1", "key2", "value2")
	f.Add("query", `SELECT * WHERE x = "y"`, "path", `C:\Users\test`)
	f.Add("unicode", "héllo\nwörld", "null\x00byte", "a\tb")
	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		item := WFDataItem{
			{Key: k1, Value: v1},
			{Key: k2, Value: v2},
		}
		b, err := item.MarshalJSON()
		if err != nil {
			return
		}
		if !json.Valid(b) {
			t.Fatalf("WFDataItem.MarshalJSON produced invalid JSON: %s", string(b))
		}
		var roundTrip map[string]string
		if err := json.Unmarshal(b, &roundTrip); err != nil {
			t.Fatalf("WFDataItem.MarshalJSON unmarshal failed: %v\nbody: %s", err, string(b))
		}
	})
}

func TestWFDataItem_MarshalJSON_EscapesSpecialChars(t *testing.T) {
	item := WFDataItem{
		{Key: "name", Value: `O'Brien`},
		{Key: "query", Value: `SELECT * WHERE x = "y"`},
		{Key: "path", Value: `C:\Users\test`},
	}
	b, err := item.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid JSON: %v (body=%s)", err, string(b))
	}
	if out["query"] != `SELECT * WHERE x = "y"` {
		t.Errorf("value not escaped, got %q", out["query"])
	}
	if out["path"] != `C:\Users\test` {
		t.Errorf("backslash not escaped, got %q", out["path"])
	}
}

func TestMarshalJSON(t *testing.T) {
	b := new(bytes.Buffer)
	err := marshalTimeseriesJSON(b, testDataSet(), &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if strings.TrimSpace(b.String()) != strings.TrimSpace(testDataJSONMinified) {
		t.Error("unexpected json body\n", b.String(), "\nexpected\n", testDataJSONMinified)
	}
}

func TestMarshalXSV(t *testing.T) {
	b := new(bytes.Buffer)
	err := marshalTimeseriesXSV(b, testDataSet(), &timeseries.RequestOptions{},
		false, false, ',')
	if err != nil {
		t.Error(err)
	}
	if strings.TrimSpace(b.String()) != strings.TrimSpace(testDataCSV) {
		t.Error("unexpected json body\n" + b.String() + "\nexpected\n" + testDataCSV)
	}
}

func TestMarshalTimeseries(t *testing.T) {
	b, err := MarshalTimeseries(testDataSet(), &timeseries.RequestOptions{OutputFormat: 5}, 200)
	if err != nil {
		t.Error(err)
	}
	if string(b) != testDataTSVWithNamesAndTypes {
		t.Errorf("unexpected output:\n%s", string(b))
	}
}
