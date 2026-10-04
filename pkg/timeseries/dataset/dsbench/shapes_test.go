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

// Package dsbench measures what DataSets cost to build, cache and keep live, across the shapes
// the providers and listeners produce. It builds every shape through public constructors only.
package dsbench

import (
	"strconv"
	"strings"
	"time"

	chmodel "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	prommodel "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	benchStep    = time.Minute
	rowBlobBytes = 57
	hostTag      = "host"
	metricName   = "cpu"
)

var benchStart = time.Unix(1_700_000_000, 0).UTC()

// shape is one kind of DataSet: how to build it from its wire or row form, and how to cache it
type shape struct {
	name   string
	series int
	points int
	// build returns a new DataSet each call; nothing is shared between calls
	build     func() timeseries.Timeseries
	marshal   timeseries.MarshalerFunc
	unmarshal timeseries.UnmarshalerFunc
}

func (s shape) pointCount() int {
	return s.series * s.points
}

func shapes() []shape {
	return []shape{
		promShape("prom-matrix", 100, 1000),
		promShape("prom-highcard", 10000, 10),
		clickHouseShape("clickhouse-grouped", 100, 1000),
		influxQLShape("influxql-mixed", 100, 1000),
		rowShape("native-rows", 100, 1000, rowBlobFields, addRowBlob),
		rowShape("typed-rows", 100, 1000, typedFields, addTypedValues),
	}
}

func trq(statement string) *timeseries.TimeRangeQuery {
	end := benchStart.Add(benchStep * 999)
	return &timeseries.TimeRangeQuery{
		Statement: statement,
		Extent:    timeseries.Extent{Start: benchStart, End: end},
		Step:      benchStep,
		StepNS:    benchStep.Nanoseconds(),
	}
}

func mustDecode(u timeseries.UnmarshalerFunc, body []byte, q *timeseries.TimeRangeQuery) timeseries.Timeseries {
	ts, err := u(body, q)
	if err != nil {
		panic(err)
	}
	return ts
}

func promShape(name string, series, points int) shape {
	m := prommodel.NewModeler()
	body := []byte(promMatrixBody(series, points))
	return shape{
		name: name, series: series, points: points,
		build:   func() timeseries.Timeseries { return mustDecode(m.WireUnmarshaler, body, trq("up")) },
		marshal: m.CacheMarshaler, unmarshal: m.CacheUnmarshaler,
	}
}

func promMatrixBody(series, points int) string {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := range series {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metric":{"__name__":"up","job":"node","instance":"host-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`:9100"},"values":[`)
		for j := range points {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('[')
			b.WriteString(strconv.FormatInt(benchStart.Add(benchStep*time.Duration(j)).Unix(), 10))
			b.WriteString(`,"`)
			b.WriteString(strconv.FormatFloat(float64((i*j)%977)/7, 'f', -1, 64))
			b.WriteString(`"]`)
		}
		b.WriteString("]}")
	}
	b.WriteString("]}}")
	return b.String()
}

func clickHouseShape(name string, series, points int) shape {
	m := chmodel.NewModeler()
	var b strings.Builder
	b.WriteString("t\thostname\tavg_query\tavg_threads\tqueries\n")
	b.WriteString("UInt64\tString\tFloat64\tFloat64\tInt64\n")
	for j := range points {
		ms := strconv.FormatInt(benchStart.Add(benchStep*time.Duration(j)).UnixMilli(), 10)
		for i := range series {
			b.WriteString(ms)
			b.WriteString("\thost-")
			b.WriteString(strconv.Itoa(i))
			b.WriteByte('\t')
			b.WriteString(strconv.FormatFloat(float64(i*j%1013)/3, 'f', -1, 64))
			b.WriteByte('\t')
			b.WriteString(strconv.Itoa(i + j))
			b.WriteByte('\t')
			b.WriteString(strconv.Itoa(i * j))
			b.WriteByte('\n')
		}
	}
	body := []byte(b.String())
	newTRQ := func() *timeseries.TimeRangeQuery {
		q := trq("SELECT t, hostname, avg_query, avg_threads, queries")
		q.TimestampDefinition = timeseries.FieldDefinition{Name: "t", DataType: timeseries.DateTimeUnixMilli}
		q.TagFieldDefintions = []timeseries.FieldDefinition{{Name: "hostname"}}
		return q
	}
	return shape{
		name: name, series: series, points: points,
		build: func() timeseries.Timeseries {
			return mustDecode(chmodel.UnmarshalTimeseries, body, newTRQ())
		},
		marshal: m.CacheMarshaler, unmarshal: m.CacheUnmarshaler,
	}
}

func influxQLShape(name string, series, points int) shape {
	m := influxql.NewModeler()
	var b strings.Builder
	b.WriteString(`{"results":[{"statement_id":0,"series":[`)
	for i := range series {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"cpu","tags":{"host":"host-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"},"columns":["time","usage","count","state"],"values":[`)
		for j := range points {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('[')
			b.WriteString(strconv.FormatInt(benchStart.Add(benchStep*time.Duration(j)).UnixNano(), 10))
			b.WriteByte(',')
			// a float field that JSON writes without a decimal point for whole values
			b.WriteString(strconv.FormatFloat(float64((i*j)%50)/4, 'f', -1, 64))
			b.WriteByte(',')
			b.WriteString(strconv.Itoa(i + j))
			b.WriteString(`,"`)
			if j%3 == 0 {
				b.WriteString("idle")
			} else {
				b.WriteString("busy")
			}
			b.WriteString(`"]`)
		}
		b.WriteString("]}")
	}
	b.WriteString("]}]}")
	body := []byte(b.String())
	return shape{
		name: name, series: series, points: points,
		build:   func() timeseries.Timeseries { return mustDecode(m.WireUnmarshaler, body, trq("SELECT * FROM cpu")) },
		marshal: m.CacheMarshaler, unmarshal: m.CacheUnmarshaler,
	}
}

// rowShape builds a DataSet in the Builder's row mode, as the native listeners and SQL decoders do
func rowShape(name string, series, points int, values timeseries.FieldDefinitions,
	addValues func(*dataset.RowBuilder, []byte, int),
) shape {
	fields := timeseries.SeriesFields{
		Timestamp: timeseries.FieldDefinition{Name: "time", Role: timeseries.RoleTimestamp},
		Tags:      timeseries.FieldDefinitions{{Name: hostTag, Role: timeseries.RoleTag}},
		Values:    values,
	}
	hosts := make([][]byte, series)
	for i := range hosts {
		hosts[i] = []byte("host-" + strconv.Itoa(i))
	}
	blob := []byte(strings.Repeat("r", rowBlobBytes))
	return shape{
		name: name, series: series, points: points,
		build: func() timeseries.Timeseries {
			b := dataset.NewBuilder(trq(metricName), dataset.BuilderOptions{Fields: fields, SeriesName: metricName})
			for j := range points {
				e := epoch.Epoch(benchStart.Add(benchStep * time.Duration(j)).UnixNano())
				for i := range series {
					row := b.Row()
					row.SetEpoch(e)
					row.SetTag(0, hosts[i])
					addValues(row, blob, i*points+j)
					if err := row.Commit(); err != nil {
						panic(err)
					}
				}
			}
			ds, err := b.Finish()
			if err != nil {
				panic(err)
			}
			return ds
		},
		marshal: dataset.MarshalDataSet, unmarshal: dataset.UnmarshalDataSet,
	}
}

// a listener's row: one opaque blob per point
var rowBlobFields = timeseries.FieldDefinitions{{Name: "row", Role: timeseries.RoleValue, DataType: timeseries.Byte}}

var typedFields = timeseries.FieldDefinitions{
	{Name: "a", Role: timeseries.RoleValue, DataType: timeseries.Int64},
	{Name: "b", Role: timeseries.RoleValue, DataType: timeseries.Float64},
	{Name: "c", Role: timeseries.RoleValue, DataType: timeseries.String},
}

func addRowBlob(row *dataset.RowBuilder, blob []byte, n int) {
	blob[n%rowBlobBytes] = byte('a' + n%26)
	row.AddBytes(blob)
}

var states = [...]string{"idle", "busy", "wait"}

func addTypedValues(row *dataset.RowBuilder, _ []byte, n int) {
	row.AddValue(int64(n))
	row.AddValue(float64(n) / 8)
	row.AddValue(states[n%len(states)])
}
