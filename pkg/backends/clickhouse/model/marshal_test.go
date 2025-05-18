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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

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
