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

package flux

import (
	"bytes"
	"testing"
)

const jsonExpected = `{"results":[{"tables":[{"columns":[{"name":"result",` +
	`"datatype":"string"},{"name":"table","datatype":"long"},{"name":"_start"` +
	`,"datatype":"dateTime:RFC3339"},{"name":"_stop",` +
	`"datatype":"dateTime:RFC3339"},{"name":"_time",` +
	`"datatype":"dateTime:RFC3339"},{"name":"avg_query","datatype":"double"},` +
	`{"name":"avg_global_thread","datatype":"double"},{"name":"hostname",` +
	`"datatype":"string"},{"name":"_measurement","datatype":"string"}],` +
	`"records":[{"values":{"result":"_result","table":0,` +
	`"_start":"2020-01-01T00:00:00Z","_stop":"2020-01-01T00:02:00Z",` +
	`"_time":"2020-01-01T00:00:00Z","avg_query":1.781,` +
	`"avg_global_thread":1.781,"hostname":"localhost","_measurement":"cpu"}},` +
	`{"values":{"result":"_result","table":0,"_start":"2020-01-01T00:00:00Z",` +
	`"_stop":"2020-01-01T00:02:00Z","_time":"2020-01-01T00:01:00Z",` +
	`"avg_query":2.429,"avg_global_thread":2.429,"hostname":"localhost",` +
	`"_measurement":"cpu"}},{"values":{"result":"_result","table":0,` +
	`"_start":"2020-01-01T00:00:00Z","_stop":"2020-01-01T00:02:00Z",` +
	`"_time":"2020-01-01T00:02:00Z","avg_query":1.929,` +
	`"avg_global_thread":1.929,"hostname":"localhost","_measurement":"cpu"}}]` +
	`}]}]}`

func TestMarshalTimeseriesJSONWriter(t *testing.T) {
	buf := new(bytes.Buffer)
	err := marshalTimeseriesJSONWriter(testDataSet(), nil, 200, buf)
	if err != nil {
		t.Error(err)
	}
	b := buf.Bytes()
	if string(b) != jsonExpected {
		t.Errorf("expected %s\n\ngot %s", jsonExpected, string(b))
	}
}
