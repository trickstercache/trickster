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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testDoc01 = `{"results":[{"statement_id":0,"series":[` +
	`{"name":"trickster","columns":["time","value","extra"],` +
	`"values":[[0,0,1],[1577836800000,0.484,1],[1577836815000,0.452,1],` +
	`[1577836830000,0.410,0],[1577836845000,0.649,0]]},` +
	`{"name":"trickster2","columns":["time","value","extra"],` +
	`"values":[[0,0,1],[1577836800000,0.484,1],[1577836815000,0.452,1],` +
	`[1577836830000,0.410,0],[1577836845000,0.649,0]]}` +
	`]},` +
	`{"statement_id":1,"series":[` +
	`{"name":"trickster","columns":["time","value","extra"],` +
	`"values":[[0,0,1],[1577836800000,0.484,1],[1577836815000,0.452,1],` +
	`[1577836830000,0.410,0],[1577836845000,0.649,0]]},` +
	`{"name":"trickster2","columns":["time","value","extra"],` +
	`"values":[[0,0,1],[1577836800000,0.484,1],[1577836815000,0.452,1],` +
	`[1577836830000,0.410,0],[1577836845000,0.649,0]]}` +
	`]}` +
	`]}`

const testDocInvalid01 = `{"results":[{"statement_id":0,"series":[` +
	`{"name":"trickster","columns":["time","value"],` +
	`"values":[["z",0]]}]}]}`

func TestUnmarshalTimeseries(t *testing.T) {

	_, err := UnmarshalTimeseries([]byte(testDoc01), nil)
	if err != timeseries.ErrNoTimerangeQuery {
		t.Error("expected ErrNoTimerangeQuery got", err)
	}

	trq := &timeseries.TimeRangeQuery{
		Statement: "hello",
	}

	_, err = UnmarshalTimeseries([]byte(testDoc01), trq)
	if err != nil {
		t.Error(err)
	}

	_, err = UnmarshalTimeseries([]byte(testDocInvalid01), trq)
	if err != timeseries.ErrInvalidTimeFormat {
		t.Error("expected ErrInvalidTimeFormat, got", err)
	}
}

func TestPointFromValues(t *testing.T) {

	v := make([]interface{}, 6)

	v[0] = int64(1577836800000)
	// v[1] will remain nil to cover the continuation case
	v[2] = "trickster"
	v[3] = true
	v[4] = float64(3.14)
	v[5] = 8480

	_, _, err := pointFromValues(v, 0)
	if err != nil {
		t.Error(err)
	}

	v[1] = &v[5] // this tests unsupported value types
	_, _, err = pointFromValues(v, 0)
	if err != timeseries.ErrInvalidTimeFormat {
		t.Error("expected ErrInvalidTimeFormat, got", err)
	}
}
