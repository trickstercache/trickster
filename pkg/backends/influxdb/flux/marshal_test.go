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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestValidateMarshalerOptions(t *testing.T) {
	_, _, _, err := validateMarshalerOptions(nil, nil)
	if err != timeseries.ErrUnknownFormat {
		t.Errorf("expected timeseries.ErrUnknownFormat, got %s", err)
	}
	ds := &dataset.DataSet{}
	ro := &timeseries.RequestOptions{}
	_, _, _, err = validateMarshalerOptions(ds, ro)
	if err != nil {
		t.Error(err)
	}
}

var testTRQ = &timeseries.TimeRangeQuery{
	Statement: fqAbsoluteTimeTokenized,
	Extent: timeseries.Extent{
		Start: time.Unix(1577836800, 0),
		End:   time.Unix(1577836920, 0),
	},
	Step:   time.Second * 60,
	StepNS: (time.Second * 60).Nanoseconds(),
	TimestampDefinition: timeseries.FieldDefinition{
		Name: timeAltColumnName,
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
								"hostname":     "localhost",
								"_measurement": "cpu",
							},
							TimestampField: timeseries.FieldDefinition{
								Name:           timeAltColumnName,
								DataType:       timeseries.DateTimeRFC3339,
								SDataType:      TypeRFC3339,
								Role:           timeseries.RoleTimestamp,
								OutputPosition: 5,
							},
							TagFieldsList: []timeseries.FieldDefinition{
								{
									Name:           "hostname",
									OutputPosition: 8,
									SDataType:      TypeString,
									Role:           timeseries.RoleTag,
								},
								{
									Name:           resultColumnName,
									OutputPosition: 1,
									SDataType:      TypeString,
									DataType:       timeseries.String,
									Role:           timeseries.RoleTag,
									DefaultValue:   "_result",
								},
								{
									Name:           "_measurement",
									OutputPosition: 9,
									SDataType:      TypeString,
									DataType:       timeseries.String,
									Role:           timeseries.RoleTag,
								},
							},
							ValueFieldsList: []timeseries.FieldDefinition{
								{
									Name:           "avg_query",
									OutputPosition: 6,
									SDataType:      TypeDouble,
									DataType:       timeseries.Float64,
									Role:           timeseries.RoleValue,
								},
								{
									Name:           "avg_global_thread",
									OutputPosition: 7,
									SDataType:      TypeDouble,
									DataType:       timeseries.Float64,
									Role:           timeseries.RoleValue,
								},
							},
							UntrackedFieldsList: []timeseries.FieldDefinition{
								{
									// role for empty CSV first column
									Role: timeseries.RoleUntracked,
								},
								{
									Name:           tableColumnName,
									OutputPosition: 2,
									Role:           timeseries.RoleUntracked,
									SDataType:      TypeLong,
									DataType:       timeseries.Int64,
								},
								{
									Name:           startColumnName,
									DataType:       timeseries.DateTimeRFC3339,
									SDataType:      TypeRFC3339,
									Role:           timeseries.RoleUntracked,
									OutputPosition: 3,
								},
								{
									Name:           stopColumnName,
									DataType:       timeseries.DateTimeRFC3339,
									SDataType:      TypeRFC3339,
									Role:           timeseries.RoleUntracked,
									OutputPosition: 4,
								},
							},
						},
						Points: []dataset.Point{
							{
								Epoch:  1577836800000000000,
								Values: []any{1.781, 54.12348},
							},
							{
								Epoch:  1577836860000000000,
								Values: []any{2.429, 57.91308},
							},
							{
								Epoch:  1577836920000000000,
								Values: []any{1.929, 55.21703},
							},
						},
					},
				},
			},
		},
	}
}

const testDataSetAsCSV = `#datatype,string,long,dateTime:RFC3339,dateTime:RFC3339,dateTime:RFC3339,double,double,string,string
#group,false,false,true,true,false,false,false,true,true
#default,_result,,,,,,,,
,result,table,_start,_stop,_time,avg_query,avg_global_thread,hostname,_measurement
,,0,2020-01-01T00:00:00Z,2020-01-01T00:02:00Z,2020-01-01T00:00:00Z,1.781,54.12348,localhost,cpu
,,0,2020-01-01T00:00:00Z,2020-01-01T00:02:00Z,2020-01-01T00:01:00Z,2.429,57.91308,localhost,cpu
,,0,2020-01-01T00:00:00Z,2020-01-01T00:02:00Z,2020-01-01T00:02:00Z,1.929,55.21703,localhost,cpu
`
