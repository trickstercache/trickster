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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

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

const testDataJSONMinified = `{"meta":[{"name":"t","type":"UInt64"},{"name":"` +
	`hostname","type":"String"},{"name":"avg_query","type":"Float64"},{"name"` +
	`:"avg_global_thread","type":"Float64"}],"data":[{"t":"1577836800000","ho` +
	`stname":"localhost","avg_query":"1","avg_global_thread":"54"},{"t":"1577` +
	`836860000","hostname":"localhost","avg_query":"1","avg_global_thread":"2` +
	`7"},{"t":"1577836920000","hostname":"localhost","avg_query":"1","avg_glo` +
	`bal_thread":"39"}],"rows":3}`

var testTRQ = &timeseries.TimeRangeQuery{
	Statement: testStatement,
	Extent: timeseries.Extent{
		Start: time.Unix(1577836800, 0),
		End:   time.Unix(1577836920, 0),
	},
	Step:   time.Second * 60,
	StepNS: (time.Second * 60).Nanoseconds(),
	TimestampDefinition: timeseries.FieldDefinition{
		Name:     "t",
		DataType: timeseries.DateTimeUnixMilli,
	},
	TagFieldDefintions: []timeseries.FieldDefinition{
		{
			Name: "hostname",
		},
	},
}

func testDataSet() *dataset.DataSet {
	return &dataset.DataSet{
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
							TimestampField: timeseries.FieldDefinition{
								Name:      "t",
								DataType:  timeseries.DateTimeUnixMilli,
								SDataType: "UInt64",
								Role:      timeseries.RoleTimestamp,
							},
							TagFieldsList: []timeseries.FieldDefinition{
								{
									Name:           "hostname",
									OutputPosition: 1,
									SDataType:      "String",
									Role:           timeseries.RoleTag,
								},
							},
							ValueFieldsList: []timeseries.FieldDefinition{
								{
									Name:           "avg_query",
									OutputPosition: 2,
									SDataType:      "Float64",
									Role:           timeseries.RoleValue,
								},
								{
									Name:           "avg_global_thread",
									OutputPosition: 3,
									SDataType:      "Float64",
									Role:           timeseries.RoleValue,
								},
							},
						},
						Points: []dataset.Point{
							{
								Epoch:  1577836800000000000,
								Values: []any{"1", "54"},
							},
							{
								Epoch:  1577836860000000000,
								Values: []any{"1", "27"},
							},
							{
								Epoch:  1577836920000000000,
								Values: []any{"1", "39"},
							},
						},
					},
				},
			},
		},
	}
}
