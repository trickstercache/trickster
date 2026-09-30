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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func decoderTRQ() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: `from(bucket:"b") |> range(start: <$TIME_TOKEN$>)`,
		Extent: timeseries.Extent{Start: time.Date(2025, 5, 4, 22, 0, 0, 0, time.UTC),
			End: time.Date(2025, 5, 4, 23, 0, 0, 0, time.UTC)},
		Step: 15 * time.Second,
	}
}

const (
	cpuAnnotations = "#datatype,string,long,dateTime:RFC3339,dateTime:RFC3339,dateTime:RFC3339,double,string,string,string\n" +
		"#group,false,false,true,true,false,false,true,true,true\n" +
		"#default,_result,,,,,,,,\n" +
		",result,table,_start,_stop,_time,_value,_field,_measurement,host\n"
	cpuRange = "2025-05-04T22:00:00Z,2025-05-04T23:00:00Z,"
)

func cpuRow(table int, at, value, field, host string) string {
	return ",," + strconv.Itoa(table) + "," + cpuRange + "2025-05-04T22:" + at + "Z," + value + "," + field + ",cpu," +
		host + "\n"
}

// bodies the stream decoder must decode exactly as the decoder it replaced did
var legacyBodies = map[string]string{
	"response":     testFluxResponseCSV1,
	"data set":     testDataSetAsCSV,
	"two tables":   testFluxResponseCSV1 + "\n\n" + secondTableCSV,
	"one schema":   cpuAnnotations + cpuRow(0, "00:00", "1.5", "usage", "a") + cpuRow(0, "00:15", "2", "usage", "a") + cpuRow(1, "00:00", "3", "usage", "b") + cpuRow(2, "00:00", "4", "idle", "a"),
	"interleaved":  cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a") + cpuRow(1, "00:00", "2", "usage", "b") + cpuRow(0, "00:15", "3", "usage", "a"),
	"results":      strings.Replace(cpuAnnotations+cpuRow(0, "00:00", "1", "usage", "a"), "\n,,0,", "\n,_result,0,", 1) + strings.Replace(cpuRow(1, "00:00", "2", "usage", "a"), ",,1,", ",mean,1,", 1),
	"empty values": cpuAnnotations + cpuRow(0, "00:00", "", "usage", "a") + cpuRow(0, "00:15", "x", "usage", "a") + cpuRow(0, "00:30", "NaN", "usage", "a"),
	"empty tags":   cpuAnnotations + cpuRow(0, "00:00", "1", "", "") + cpuRow(1, "00:00", "2", "usage", ""),
	"bad times":    cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a") + ",,0," + cpuRange + "nope,2,usage,cpu,a\n" + ",,1," + cpuRange + "nope,3,usage,cpu,b\n",
	"mis-sized":    cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a") + ",,0,short\n" + cpuRow(0, "00:15", "2", "usage", "a") + cpuRow(0, "00:30", "3", "usage", "a")[:20] + ",x,y,z,w,v,u,t,s,r\n",
	"types": "#datatype,string,long,dateTime:RFC3339Nano,long,unsignedLong,double,bool,string,duration,dateTime:RFC3339,null,mystery\n" +
		"#group,false,false,false,false,false,false,false,true,false,false,false,false\n" +
		"#default,_result,,,,,,,,,,,\n" +
		",result,table,_time,l,u,d,b,s,dur,dt,n,m\n" +
		",,0,2025-05-04T22:00:00.5Z,-7,7,1.25,true,x,1h,2025-05-04T22:00:00Z,x,y\n" +
		",,0,2025-05-04T22:00:01.123456789Z,x,-1,1e400,maybe,y,,,,\n" +
		",,0,2025-05-04T22:00:02+01:00,9223372036854775808,18446744073709551615,-Inf,F,\"a,b\",2m,z,,\n",
	"quoted": "#datatype,string,long,dateTime:RFC3339,string,string\n#group,false,false,false,false,true\n#default,_result,,,,\n" +
		",result,table,_time,_value,host\n" +
		",,0,2025-05-04T22:00:00Z,\"a,b\",\"h\"\"1\"\n,,0,2025-05-04T22:00:01Z,\"multi\nline\",\"h\"\"1\"\n",
	"crlf":         strings.ReplaceAll(cpuAnnotations+cpuRow(0, "00:00", "1", "usage", "a")+cpuRow(0, "00:15", "2", "usage", "a"), "\n", "\r\n"),
	"time column":  strings.Replace(cpuAnnotations, ",_time,", ",time,", 1) + cpuRow(0, "00:00", "1", "usage", "a"),
	"long time":    strings.Replace(cpuAnnotations, "dateTime:RFC3339,double", "long,double", 1) + cpuRow(0, "00:00", "1", "usage", "a"),
	"no rows":      cpuAnnotations,
	"empty table":  cpuAnnotations + "\n" + strings.Replace(secondTableCSV, "#datatype,string,long,dateTime:RFC3339,double", "#datatype,string,long,dateTime:RFC3339,long", 1),
	"unnamed tag":  "#datatype,string,long,dateTime:RFC3339,double,string\n#group,false,false,false,false,true\n#default,_result,,,,\n,result,table,_time,_value,\n,,0,2025-05-04T22:00:00Z,1,x\n",
	"defaults":     "#datatype,string,long,dateTime:RFC3339,double,string\n#group,false,false,false,false,true\n#default,_result,,,0,dflt\n,result,table,_time,_value,host\n,,0,2025-05-04T22:00:00Z,,\n",
	"leading rows": "\n\n" + cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a"),
}

const secondTableCSV = `#datatype,string,long,dateTime:RFC3339,double,string,string
#group,false,false,false,false,true,true
#default,_result,,,,,
,result,table,_time,_value,_field,_measurement
,,1,2025-05-04T22:29:00Z,5,usage_system,cpu
`

func conformance(t *testing.T, body string) {
	t.Helper()
	streamtest.Conformance(t, newDecoder, streamtest.Case{
		TRQ: decoderTRQ(), Body: []byte(body), Legacy: legacyUnmarshalTimeseriesReader,
	})
}

func TestDecoderMatchesLegacy(t *testing.T) {
	for name, body := range legacyBodies {
		t.Run(name, func(t *testing.T) { conformance(t, body) })
	}
}

func TestDecoderMatchesLegacyAtScale(t *testing.T) {
	rng := weaktest.NewRand(7, 7)
	for trial := range 20 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) { conformance(t, string(randomBody(rng))) })
	}
}

// randomBody writes tables of random schemas, each a run of series in time order
func randomBody(rng *weaktest.Rand) []byte {
	types := []string{TypeDouble, TypeLong, TypeUnsignedLong, TypeBool, TypeString}
	var b strings.Builder
	table := 0
	for range 1 + rng.IntN(3) {
		dt := types[rng.IntN(len(types))]
		b.WriteString("#datatype,string,long,dateTime:RFC3339," + dt + ",string,string\n")
		b.WriteString("#group,false,false,false,false,true,true\n#default,_result,,,,,\n")
		b.WriteString(",result,table,_time,_value,_field,host\n")
		for range rng.IntN(4) {
			// a run's own host, as a run that repeated one would restart its series' times
			host := "h" + strconv.Itoa(table)
			for p := range rng.IntN(30) {
				v := strconv.Itoa(rng.IntN(100))
				switch dt {
				case TypeDouble:
					v = strconv.FormatFloat(rng.Float64(), 'f', -1, 64)
				case TypeBool:
					v = strconv.FormatBool(rng.IntN(2) == 0)
				case TypeString:
					v = "s" + v
				}
				if rng.IntN(10) == 0 {
					v = ""
				}
				at := time.Date(2025, 5, 4, 22, 0, p, 0, time.UTC).Format(time.RFC3339)
				b.WriteString(",," + strconv.Itoa(table) + "," + at + "," + v + ",f" + dt + "," + host + "\n")
			}
			table++
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func TestDecoderErrors(t *testing.T) {
	bodies := map[string]string{
		"empty":             "",
		"three records":     "#datatype,string,long,dateTime:RFC3339,double\n#group,false,false,false,false\n#default,_result,,,\n",
		"not annotated":     ",result,table,_time,_value\n,,0,2025-05-04T22:00:00Z,1\n,,0,2025-05-04T22:00:01Z,1\n,,0,2025-05-04T22:00:02Z,1\n",
		"no group":          "#datatype,string,long,dateTime:RFC3339,double\n#default,_result,,,\n,result,table,_time,_value\n,,0,2025-05-04T22:00:00Z,1\n",
		"no default":        "#datatype,string,long,dateTime:RFC3339,double\n#group,false,false,false,false\n,result,table,_time,_value\n,,0,2025-05-04T22:00:00Z,1\n",
		"narrow header":     "#datatype,string,long,dateTime:RFC3339\n#group,false,false,false\n#default,_result,,\n,result,_time\n,,2025-05-04T22:00:00Z\n",
		"ragged annotation": "#datatype,string,long,dateTime:RFC3339,double\n#group,false,false,false\n#default,_result,,,\n,result,table,_time,_value\n,,0,2025-05-04T22:00:00Z,1\n",
		"no time":           "#datatype,string,long,double,double\n#group,false,false,false,false\n#default,_result,,,\n,result,table,a,b\n,,0,1,2\n",
		"cut table":         cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a") + "#datatype,string,long\n#group,false,false\n",
		"table cut early":   cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a") + "#datatype,string,long\n#datatype,string,long\n",
		"headless table":    "#datatype,string,long,dateTime:RFC3339,double\n" + cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a"),
		"error table":       cpuAnnotations + cpuRow(0, "00:00", "1", "usage", "a") + "\n#datatype,string,string\n#group,true,true\n#default,,\n,error,reference\n,query failed,897\n",
		"open quote":        cpuAnnotations + ",,0,\"x\n",
		"bare quote":        cpuAnnotations + ",,0,x\"y\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), WantErr: streamtest.ErrAny,
				Legacy: legacyUnmarshalTimeseriesReader,
			})
		})
	}
	_, err := UnmarshalTimeseries([]byte(cpuAnnotations), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
}

func decode(t *testing.T, body string) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseries([]byte(body), decoderTRQ())
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func TestDecoderDepartures(t *testing.T) {
	t.Run("a series' rows are sorted", func(t *testing.T) {
		s := decode(t, cpuAnnotations+cpuRow(0, "00:30", "3", "usage", "a")+cpuRow(0, "00:00", "1", "usage", "a")).
			Results[0].SeriesList[0]
		require.True(t, s.IsSorted())
		require.Equal(t, []any{1.0}, s.Points()[0].Values)
	})
	t.Run("tables of one series are one series", func(t *testing.T) {
		ds := decode(t, cpuAnnotations+cpuRow(0, "00:15", "2", "usage", "a")+"\n"+cpuAnnotations+
			cpuRow(1, "00:00", "1", "usage", "a"))
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Equal(t, 2, ds.Results[0].SeriesList[0].PointCount())
	})
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	// a decoded DataSet must not change when the buffer it was decoded from is reused
	for _, body := range [][]byte{fluxBody(20, 30), []byte(legacyBodies["quoted"]), []byte(legacyBodies["types"])} {
		pristine := bytes.Clone(body)
		ts, err := UnmarshalTimeseries(body, decoderTRQ())
		require.NoError(t, err)
		for i := range body {
			body[i] = 'x'
		}
		want, err := UnmarshalTimeseries(pristine, decoderTRQ())
		require.NoError(t, err)
		require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), ts.(*dataset.DataSet),
			streamtest.CompareOptions{}))
	}
}

// fluxBody writes one table of series, each a host's points, as InfluxDB writes a range query's
func fluxBody(series, points int) []byte {
	var b strings.Builder
	b.WriteString(cpuAnnotations)
	start := time.Date(2025, 5, 4, 22, 0, 0, 0, time.UTC)
	for s := range series {
		prefix := ",," + strconv.Itoa(s) + "," + cpuRange
		suffix := ",usage_user,cpu,host-" + strconv.Itoa(s) + "\n"
		for p := range points {
			b.WriteString(prefix + start.Add(time.Duration(p)*15*time.Second).Format(time.RFC3339) + "," +
				strconv.FormatFloat(float64((s*p)%97)/7, 'f', -1, 64) + suffix)
		}
	}
	return []byte(b.String())
}

func BenchmarkDecoder(b *testing.B) {
	trq := decoderTRQ()
	for _, shape := range []struct {
		name           string
		series, points int
	}{
		{"100x1000", 100, 1000},
		{"10000x10", 10000, 10},
	} {
		body := fluxBody(shape.series, shape.points)
		b.Run(shape.name+"/legacy", func(b *testing.B) { streamtest.Bench(b, legacyUnmarshalTimeseriesReader, trq, body) })
		b.Run(shape.name+"/stream", func(b *testing.B) {
			streamtest.Bench(b, stream.ReaderUnmarshaler(newDecoder), trq, body)
		})
	}
}
