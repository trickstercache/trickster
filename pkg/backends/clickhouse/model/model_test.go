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
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestTestColString(t *testing.T) {
	c := col{
		name:  "test",
		val:   "yes",
		quote: "`",
	}
	expected := "			\"test\": `yes`"
	got := c.String()

	if expected != got {
		t.Errorf("expected: %s\ngot:      %s", expected, got)
	}
}

func TestTestColsString(t *testing.T) {
	c := cols{
		col{
			name:  "test",
			val:   "yes",
			quote: "`",
		},
		col{
			name:  "test2",
			val:   "no",
			quote: "|",
		},
	}

	expected := "		{\n			\"test\": `yes`,\n			\"test2\": |no|\n		}"
	got := c.String()

	if expected != got {
		t.Errorf("expected: %s\ngot:      %s", expected, got)
	}
}

func TestNewModeler(t *testing.T) {
	m := NewModeler()
	if m == nil || m.CacheMarshaler == nil {
		t.Error("failed to get valid modeler")
	}
}

const testStatement = `SELECT (intDiv(toUInt32(dt), 60) * 60) * 1000 AS t, ` +
	`hostname, avg(query) avg_query, avg(global_thread) avg_global_thread ` +
	`FROM trickster.metrics_history WHERE <$RANGE$> ` +
	`GROUP BY t, hostname ORDER BY t, hostname FORMAT <$FORMAT$>`

const testDataTSV = `1577836800000	localhost	1	54
1577836860000	localhost	1	27
1577836920000	localhost	1	39
`

const testDataTSVWithNames = `t	hostname	avg_query	avg_global_thread
1577836800000	localhost	1	54
1577836860000	localhost	1	27
1577836920000	localhost	1	39
`

const testDataTSVWithNamesAndTypes = `t	hostname	avg_query	avg_global_thread
UInt64	String	Float64	Float64
1577836800000	localhost	1	54
1577836860000	localhost	1	27
1577836920000	localhost	1	39
`

const testDataCSV = `1577836800000,localhost,1,54
1577836860000,localhost,1,27
1577836920000,localhost,1,39
`

const testDataCSVWithNames = `t,hostname,avg_query,avg_global_thread
1577836800000,localhost,1,54
1577836860000,localhost,1,27
1577836920000,localhost,1,39
`

const testDataJSON = `{
	"meta":
	[
		{
			"name": "t",
			"type": "UInt64"
		},
		{
			"name": "hostname",
			"type": "String"
		},
		{
			"name": "avg_query",
			"type": "Float64"
		},
		{
			"name": "avg_global_thread",
			"type": "Float64"
		}
	],

	"data":
	[
		{
			"t": "1577836800000",
			"hostname": "localhost",
			"avg_query": 1,
			"avg_global_thread": 54
		},
		{
			"t": "1577836860000",
			"hostname": "localhost",
			"avg_query": 1,
			"avg_global_thread": 27
		},
		{
			"t": "1577836920000",
			"hostname": "localhost",
			"avg_query": 1,
			"avg_global_thread": 39
		}
	],
	
	"rows": 3
}
`

var testTRQ = &timeseries.TimeRangeQuery{
	Statement: testStatement,
	Extent: timeseries.Extent{
		Start: time.Unix(1577836800, 0),
		End:   time.Unix(1577836920, 0),
	},
	Step:   time.Second * 60,
	StepNS: (time.Second * 60).Nanoseconds(),
	TimestampDefinition: timeseries.FieldDefinition{
		Name:          "t",
		DataType:      1,
		ProviderData1: 1,
		SDataType:     "UInt64",
	},
	TagFieldDefintions: []timeseries.FieldDefinition{
		{
			Name: "t",
		},
		{
			Name:           "hostname",
			OutputPosition: 1,
			SDataType:      "String",
		},
	},
	ValueFieldDefinitions: []timeseries.FieldDefinition{
		{
			Name:           "avg_query",
			OutputPosition: 2,
			SDataType:      "Float64",
		},
		{
			Name:           "avg_global_thread",
			OutputPosition: 3,
			SDataType:      "Float64",
		},
	},
}

var testDataset = &dataset.DataSet{
	TimeRangeQuery: testTRQ,
	ExtentList:     timeseries.ExtentList{testTRQ.Extent},
	Results: []*dataset.Result{
		{
			SeriesList: []*dataset.Series{
				{
					Header: dataset.SeriesHeader{
						QueryStatement: testTRQ.Statement,
						Tags: dataset.Tags{
							"hostname": "localhost",
						},
					},
					Points: []dataset.Point{
						{
							Epoch:  1577836800000000000,
							Values: []interface{}{"1", "54"},
						},
						{
							Epoch:  1577836860000000000,
							Values: []interface{}{"1", "27"},
						},
						{
							Epoch:  1577836920000000000,
							Values: []interface{}{"1", "39"},
						},
					},
				},
			},
		},
	},
}

func TestMarshalJSON(t *testing.T) {
	w := httptest.NewRecorder()
	marshalTimeseriesJSON(testDataset, &timeseries.RequestOptions{}, 200, w)
	b, _ := io.ReadAll(w.Result().Body)
	if string(b) != testDataJSON {
		t.Error()
	}
}

func TestMarshalCSV(t *testing.T) {
	w := httptest.NewRecorder()
	marshalTimeseriesCSV(testDataset, &timeseries.RequestOptions{OutputFormat: 1}, 200, w)
	b, _ := io.ReadAll(w.Result().Body)
	if string(b) != testDataCSV {
		t.Error()
	}
}

func TestMarshalCSVWithNames(t *testing.T) {
	w := httptest.NewRecorder()
	marshalTimeseriesCSVWithNames(testDataset, &timeseries.RequestOptions{OutputFormat: 2}, 200, w)
	b, _ := io.ReadAll(w.Result().Body)
	if string(b) != testDataCSVWithNames {
		t.Error()
	}
}

func TestMarshalTSV(t *testing.T) {
	w := httptest.NewRecorder()
	marshalTimeseriesTSV(testDataset, &timeseries.RequestOptions{OutputFormat: 3}, 200, w)
	b, _ := io.ReadAll(w.Result().Body)
	if string(b) != testDataTSV {
		t.Error()
	}
}

func TestMarshalTSVWithNames(t *testing.T) {
	w := httptest.NewRecorder()
	marshalTimeseriesTSVWithNames(testDataset, &timeseries.RequestOptions{OutputFormat: 4}, 200, w)
	b, _ := io.ReadAll(w.Result().Body)
	if string(b) != testDataTSVWithNames {
		t.Error()
	}
}

func TestMarshalTSVWithNamesAndTypes(t *testing.T) {
	w := httptest.NewRecorder()
	marshalTimeseriesTSVWithNamesAndTypes(testDataset, &timeseries.RequestOptions{OutputFormat: 5}, 200, w)
	b, _ := io.ReadAll(w.Result().Body)
	if string(b) != testDataTSVWithNamesAndTypes {
		t.Error()
	}
}

func TestUnmarshalTimeseries(t *testing.T) {

	ts, err := UnmarshalTimeseries([]byte(testDataTSVWithNamesAndTypes), testTRQ.Clone())
	if err != nil {
		t.Error(err)
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		t.Error("expected non-nil dataset")
		return
	}

	if len(ds.ExtentList) != 1 || !ds.ExtentList[0].Start.Equal(testDataset.ExtentList[0].Start) ||
		!ds.ExtentList[0].End.Equal(testDataset.ExtentList[0].End) {
		t.Error("unexpected extents: ", ds.ExtentList)
	}

}

func TestMarshalTimeseries(t *testing.T) {
	b, err := MarshalTimeseries(testDataset, &timeseries.RequestOptions{OutputFormat: 5}, 200)
	if err != nil {
		t.Error(err)
	}
	if string(b) != testDataTSVWithNamesAndTypes {
		t.Errorf("unexpected output:\n%s", string(b))
	}

}
