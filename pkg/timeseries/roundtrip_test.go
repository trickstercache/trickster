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

package timeseries

import (
	"testing"
	"time"
)

func TestExtentRoundTrip(t *testing.T) {
	v := Extent{
		Start:    time.Unix(1000, 0).UTC(),
		End:      time.Unix(2000, 0).UTC(),
		LastUsed: time.Unix(1500, 0).UTC(),
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Extent
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if !v2.Start.Equal(v.Start) {
		t.Fatal("Start mismatch")
	}
	if !v2.End.Equal(v.End) {
		t.Fatal("End mismatch")
	}
	if !v2.LastUsed.Equal(v.LastUsed) {
		t.Fatal("LastUsed mismatch")
	}
}

func TestExtentListRoundTrip(t *testing.T) {
	v := ExtentList{
		{Start: time.Unix(100, 0).UTC(), End: time.Unix(200, 0).UTC()},
		{Start: time.Unix(300, 0).UTC(), End: time.Unix(400, 0).UTC()},
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 ExtentList
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(v2) != 2 {
		t.Fatal("expected 2 extents")
	}
	if !v2[0].Start.Equal(v[0].Start) {
		t.Fatal("first extent Start mismatch")
	}
	if !v2[1].End.Equal(v[1].End) {
		t.Fatal("last extent End mismatch")
	}
}

func TestFieldDefinitionRoundTrip(t *testing.T) {
	v := FieldDefinition{
		Name:           "temperature",
		DataType:       FieldDataType(2),
		OutputPosition: 5,
		Role:           FieldRole(1),
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 FieldDefinition
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Name != v.Name {
		t.Fatal("Name mismatch")
	}
	if v2.DataType != v.DataType {
		t.Fatal("DataType mismatch")
	}
	if v2.OutputPosition != v.OutputPosition {
		t.Fatal("OutputPosition mismatch")
	}
	if v2.Role != v.Role {
		t.Fatal("Role mismatch")
	}
}

func TestTimeRangeQueryRoundTrip(t *testing.T) {
	v := TimeRangeQuery{
		Statement:   "SELECT * FROM cpu",
		StepNS:      15000000000,
		RecordLimit: 100,
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 TimeRangeQuery
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Statement != v.Statement {
		t.Fatal("Statement mismatch")
	}
	if v2.StepNS != v.StepNS {
		t.Fatal("StepNS mismatch")
	}
	if v2.RecordLimit != v.RecordLimit {
		t.Fatal("RecordLimit mismatch")
	}
}
