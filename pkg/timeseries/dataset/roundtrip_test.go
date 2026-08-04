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

package dataset

import (
	"testing"
)

func TestDataSetRoundTrip(t *testing.T) {
	v := DataSet{
		Status: "success",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 DataSet
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Status != "success" {
		t.Fatal("Status mismatch")
	}
}

func TestSeriesHeaderRoundTrip(t *testing.T) {
	v := SeriesHeader{
		Name:           "cpu.idle",
		Tags:           Tags{"host": "server01", "region": "us-east"},
		QueryStatement: "SELECT mean(value) FROM cpu",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 SeriesHeader
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Name != v.Name {
		t.Fatal("Name mismatch")
	}
	if v2.Tags["host"] != "server01" {
		t.Fatal("Tags host mismatch")
	}
	if v2.QueryStatement != v.QueryStatement {
		t.Fatal("QueryStatement mismatch")
	}
}

func TestSeriesRoundTrip(t *testing.T) {
	v := Series{
		Header:    SeriesHeader{Name: "mem.free"},
		PointSize: 4096,
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Series
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Header.Name != "mem.free" {
		t.Fatal("Header.Name mismatch")
	}
	if v2.PointSize != 4096 {
		t.Fatal("PointSize mismatch")
	}
}

func TestResultRoundTrip(t *testing.T) {
	v := Result{
		StatementID: 7,
		Error:       "timeout",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Result
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.StatementID != 7 {
		t.Fatal("StatementID mismatch")
	}
	if v2.Error != "timeout" {
		t.Fatal("Error mismatch")
	}
}

func TestTagsRoundTrip(t *testing.T) {
	v := Tags{"host": "server01", "region": "us-east", "dc": "dc1"}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Tags
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(v2) != 3 {
		t.Fatal("expected 3 tags")
	}
	if v2["host"] != "server01" {
		t.Fatal("host mismatch")
	}
	if v2["region"] != "us-east" {
		t.Fatal("region mismatch")
	}
	if v2["dc"] != "dc1" {
		t.Fatal("dc mismatch")
	}
}
