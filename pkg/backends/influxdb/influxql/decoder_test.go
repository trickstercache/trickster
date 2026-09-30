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

package influxql

import (
	"bytes"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

const (
	t0 = "1577836800000000000"
	t1 = "1577836815000000000"
	t2 = "1577836830000000000"
)

func decoderTRQ() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: "SELECT mean(value) FROM cpu WHERE time >= '<$TIME_TOKEN$>' GROUP BY time(15s)",
		Extent:    timeseries.Extent{Start: time.Unix(1577836800, 0), End: time.Unix(1577836900, 0)},
		Step:      15 * time.Second,
	}
}

// bodies the stream decoder must decode as the decoder it replaced did, once that decoder's float64
// for every number is accounted for
var legacyBodies = map[string]string{
	"test doc": testDoc01,
	"one":      `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value"],"values":[[` + t0 + `,1.5],[` + t1 + `,2],[` + t2 + `,2.25]]}]}]}`,
	"tagged":   `{"results":[{"statement_id":0,"series":[{"name":"cpu","tags":{"host":"a","dc":"x"},"columns":["time","value"],"values":[[` + t0 + `,1]]},{"name":"cpu","tags":{"host":"b","dc":"x"},"columns":["time","value"],"values":[[` + t0 + `,2]]}]}]}`,
	"types":    `{"results":[{"statement_id":0,"series":[{"name":"m","columns":["time","f","i","s","b","n"],"values":[[` + t0 + `,1.5,7,"idle",true,null],[` + t1 + `,-0,-9007199254740992,"",false,null],[` + t2 + `,1.5e3,0,"é\n\"x\"",true,1]]}]}]}`,
	"statements": `{"results":[{"statement_id":0,"series":[{"name":"a","columns":["time","v"],"values":[[` + t0 + `,1]]}]},` +
		`{"statement_id":1,"series":[{"name":"b","columns":["time","v"],"values":[[` + t0 + `,2]]}]},{"statement_id":2}]}`,
	"errors":        `{"results":[{"statement_id":0,"error":"partial failure","series":[{"name":"a","columns":["time","v"],"values":[[` + t0 + `,1]]}]},{"statement_id":1,"error":"failed"}],"error":"top"}`,
	"unsorted":      `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value"],"values":[[` + t2 + `,3],[` + t0 + `,1],[` + t1 + `,2]]}]}]}`,
	"epoch zero":    `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value"],"values":[[` + t0 + `,1],[0,9],[` + t1 + `,2]]}]}]}`,
	"first zero":    `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value","s"],"values":[[0,9,"x"],[` + t1 + `,2,"y"]]}]}]}`,
	"first null":    `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value","s"],"values":[[` + t0 + `,null,"x"],[` + t1 + `,2,null]]}]}]}`,
	"only zero":     `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value"],"values":[[0,9]]}]}]}`,
	"rfc3339":       `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value"],"values":[["2020-01-01T00:00:00Z",1],["2020-01-01T00:00:15.5Z",2],["2020-01-01T01:00:30+01:00",3]]}]}]}`,
	"alt time":      `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["value","_time","other"],"values":[[1,` + t0 + `,"a"],[2,` + t1 + `,"b"]]}]}]}`,
	"time last":     `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["value","time"],"values":[[1,` + t0 + `],[2,` + t1 + `]]}]}]}`,
	"two times":     `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","value","_time"],"values":[[` + t0 + `,1,` + t1 + `]]}]}]}`,
	"short rows":    `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","a","b"],"values":[[` + t0 + `,1,2],[` + t1 + `,3,4]]}]}]}`,
	"unknown keys":  `{"results":[{"statement_id":0,"messages":[{"level":"warning","text":"x"}],"series":[{"name":"cpu","extra":{"a":[1,2]},"partial":true,"columns":["time","v"],"values":[[` + t0 + `,1]]}]}],"other":null}`,
	"case keys":     `{"Results":[{"Statement_ID":3,"SERIES":[{"Name":"cpu","TAGS":{"h":"a"},"Columns":["time","v"],"VALUES":[[` + t0 + `,1]],"Partial":null}],"Error":"e"}],"ERROR":"top"}`,
	"id after":      `{"results":[{"series":[{"name":"cpu","columns":["time","v"],"values":[[` + t0 + `,1]]}],"error":"e","statement_id":4}]}`,
	"no id":         `{"results":[{"series":[{"name":"cpu","columns":["time","v"],"values":[[` + t0 + `,1]]}]}]}`,
	"values first":  `{"results":[{"statement_id":0,"series":[{"values":[[` + t1 + `,2],[` + t0 + `,1]],"columns":["time","v"],"tags":{"h":"a"},"name":"cpu"}]}]}`,
	"name last":     `{"results":[{"statement_id":0,"series":[{"values":[[` + t0 + `,1]],"name":"cpu","columns":["v","time"]}]}]}`,
	"empty":         `{"results":[]}`,
	"no series":     `{"results":[{"statement_id":0}]}`,
	"null series":   `{"results":[{"statement_id":0,"series":null}]}`,
	"empty series":  `{"results":[{"statement_id":0,"series":[]}]}`,
	"empty values":  `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":[]}]}]}`,
	"null names":    `{"results":[{"statement_id":null,"series":[{"name":null,"tags":{"h":null},"columns":["time",null],"values":[[` + t0 + `,1]]}],"error":null}],"error":null}`,
	"null tags":     `{"results":[{"statement_id":0,"series":[{"name":"cpu","tags":null,"columns":["time","v"],"values":[[` + t0 + `,1]]}]}]}`,
	"empty tags":    `{"results":[{"statement_id":0,"series":[{"name":"cpu","tags":{},"columns":["time","v"],"values":[[` + t0 + `,1]]}]}]}`,
	"escapes":       `{"results":[{"statement_id":0,"series":[{"name":"cpu","tags":{"h\"x":"a\\b","é":"\t"},"columns":["time","vé"],"values":[[` + t0 + `,"😀"]]}]}]}`,
	"invalid utf8":  "{\"results\":[{\"statement_id\":0,\"series\":[{\"name\":\"c\xffu\",\"columns\":[\"time\",\"v\"],\"values\":[[" + t0 + ",\"a\xfeb\"]]}]}]}",
	"whitespace":    "\n{ \"results\" : [ { \"statement_id\" : 0 , \"series\" : [ { \"name\" : \"cpu\" , \"columns\" : [ \"time\" , \"v\" ] , \"values\" : [ [ " + t0 + " , 1 ] , [ " + t1 + " , \"x\" ] ] } ] } ] }\n",
	"float times":   `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":[[1.5,1],[1577836800e9,2],[1.5e18,3]]}]}]}`,
	"negative time": `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":[[-1000000000,1],[` + t0 + `,2]]}]}]}`,
	"exact ints":    `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":[[` + t0 + `,9007199254740992],[` + t1 + `,-4503599627370496]]}]}]}`,
}

// asLegacyTypes returns ts's DataSet with each int64 value the float64 the legacy decoder made of every
// number, so the two decoders' results can be compared
func asLegacyTypes(ts timeseries.Timeseries) *dataset.DataSet {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return nil
	}
	out := &dataset.DataSet{
		Error: ds.Error, TimeRangeQuery: ds.TimeRangeQuery, ExtentList: ds.ExtentList,
		Results: make([]*dataset.Result, len(ds.Results)),
	}
	for i, r := range ds.Results {
		nr := &dataset.Result{StatementID: r.StatementID, Name: r.Name, Error: r.Error}
		for _, s := range r.SeriesList {
			pts := s.Points()
			for j := range pts {
				for k, v := range pts[j].Values {
					if n, ok := v.(int64); ok {
						pts[j].Values[k] = float64(n)
					}
				}
			}
			nr.SeriesList = append(nr.SeriesList, dataset.NewSeries(s.Header, pts))
		}
		out.Results[i] = nr
	}
	return out
}

func conformance(t *testing.T, body string) {
	t.Helper()
	streamtest.Conformance(t, newDecoder, streamtest.Case{
		TRQ: decoderTRQ(), Body: []byte(body), Legacy: legacyUnmarshalTimeseriesReader, Unwrap: asLegacyTypes,
	})
}

func TestDecoderMatchesLegacy(t *testing.T) {
	for name, body := range legacyBodies {
		t.Run(name, func(t *testing.T) { conformance(t, body) })
	}
}

func TestDecoderMatchesLegacyAtScale(t *testing.T) {
	rng := weaktest.NewRand(4, 4)
	for trial := range 20 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) { conformance(t, randomBody(rng)) })
	}
}

// randomBody writes a response of random statements, series and rows with values of every kind
func randomBody(rng *weaktest.Rand) string {
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for st := range 1 + rng.IntN(3) {
		if st > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"statement_id":` + strconv.Itoa(st) + `,"series":[`)
		for s := range rng.IntN(6) {
			if s > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"name":"m` + strconv.Itoa(rng.IntN(3)) + `","tags":{"host":"h` + strconv.Itoa(s) +
				`"},"columns":["time","f","i","s","b"],"values":[`)
			for p := range rng.IntN(40) {
				if p > 0 {
					b.WriteByte(',')
				}
				// whole seconds, which a float64 holds exactly as nanoseconds
				b.WriteString("[" + strconv.FormatInt((1577836800+int64(rng.IntN(100000)))*1e9, 10) + ",")
				b.WriteString(randomValue(rng) + "," + randomValue(rng) + "," + randomValue(rng) + "," +
					randomValue(rng) + "]")
			}
			b.WriteString("]}")
		}
		b.WriteString("]}")
	}
	b.WriteString("]}")
	return b.String()
}

func randomValue(rng *weaktest.Rand) string {
	switch rng.IntN(6) {
	case 0:
		return strconv.FormatFloat(rng.Float64()*1000, 'f', -1, 64)
	case 1:
		return strconv.Itoa(rng.IntN(1 << 30))
	case 2:
		return `"s` + strconv.Itoa(rng.IntN(10)) + `"`
	case 3:
		return strconv.FormatBool(rng.IntN(2) == 0)
	case 4:
		return "null"
	}
	return strconv.FormatFloat(float64(rng.IntN(100))/4, 'g', -1, 64)
}

func TestDecoderErrors(t *testing.T) {
	// bodies both decoders reject; the ones the legacy decoder crashed on are in TestDecoderDepartures
	bodies := map[string]string{
		"empty":             ``,
		"null":              `null`,
		"array":             `[]`,
		"number":            `1`,
		"no results":        `{}`,
		"null results":      `{"results":null}`,
		"only error":        `{"error":"database not found"}`,
		"truncated":         `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":[[` + t0,
		"results object":    `{"results":{}}`,
		"id string":         `{"results":[{"statement_id":"0"}]}`,
		"id fraction":       `{"results":[{"statement_id":1.5}]}`,
		"id past int64":     `{"results":[{"statement_id":9223372036854775808}]}`,
		"error number":      `{"results":[],"error":5}`,
		"result error bool": `{"results":[{"statement_id":0,"error":true}]}`,
		"series object":     `{"results":[{"statement_id":0,"series":{}}]}`,
		"one column":        `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time"],"values":[]}]}]}`,
		"no columns":        `{"results":[{"statement_id":0,"series":[{"name":"cpu","values":[]}]}]}`,
		"null columns":      `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":null,"values":[]}]}]}`,
		"no values":         `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"]}]}]}`,
		"null values":       `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":null}]}]}`,
		"values object":     `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":{}}]}]}`,
		"row number":        `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","v"],"values":[5]}]}]}`,
		"name number":       `{"results":[{"statement_id":0,"series":[{"name":5,"columns":["time","v"],"values":[]}]}]}`,
		"tag number":        `{"results":[{"statement_id":0,"series":[{"name":"c","tags":{"h":1},"columns":["time","v"],"values":[]}]}]}`,
		"tags array":        `{"results":[{"statement_id":0,"series":[{"name":"c","tags":[],"columns":["time","v"],"values":[]}]}]}`,
		"column number":     `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time",1],"values":[]}]}]}`,
		"partial string":    `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[],"partial":"x"}]}]}`,
		"bad time":          `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[["z",0]]}]}]}`,
		"bool time":         `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[true,0]]}]}]}`,
		"null time":         `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[null,0]]}]}]}`,
		"minus one":         `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[-1,0]]}]}]}`,
		"array value":       `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[` + t0 + `,[1]]]}]}]}`,
		"object value":      `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[0,{}]]}]}]}`,
		"huge value":        `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[` + t0 + `,1e400]]}]}]}`,
		"huge time":         `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[1e400,1]]}]}]}`,
		"bad late row":      `{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[[` + t0 + `,1],["x",2]]}]}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), WantErr: streamtest.ErrAny,
				Legacy: legacyUnmarshalTimeseriesReader,
			})
		})
	}
	_, err := UnmarshalTimeseries([]byte(`{"results":[{"statement_id":0,"series":[{"name":"c","columns":["time","v"],"values":[["z",0]]}]}]}`), decoderTRQ())
	require.ErrorIs(t, err, timeseries.ErrInvalidTimeFormat)
	_, err = UnmarshalTimeseries([]byte(`{"results":[]}`), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	_, err = UnmarshalTimeseriesReader(bytes.NewReader(nil), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	_, err = UnmarshalTimeseriesReader(nil, decoderTRQ())
	require.ErrorIs(t, err, timeseries.ErrInvalidBody)
}

func decode(t *testing.T, body string) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseries([]byte(body), decoderTRQ())
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func series(body string) string {
	return `{"results":[{"statement_id":0,"series":[` + body + `]}]}`
}

func TestDecoderDepartures(t *testing.T) {
	t.Run("numbers are typed by their literals", func(t *testing.T) {
		ds := decode(t, series(`{"name":"c","columns":["time","v"],"values":[[`+t0+`,1],[`+t1+
			`,1.5],[`+t2+`,9007199254740993],[1577836845000000000,-0]]}`))
		pts := ds.Results[0].SeriesList[0].Points()
		require.Equal(t, int64(1), pts[0].Values[0])
		require.Equal(t, 1.5, pts[1].Values[0])
		// past 2^53, where the legacy decoder's float64 rounded it
		require.Equal(t, int64(9007199254740993), pts[2].Values[0])
		f, ok := pts[3].Values[0].(float64)
		require.True(t, ok && f == 0 && math.Signbit(f), "-0 is a float")
		// the header's type stays the float64 every number once was, so series identity holds
		require.Equal(t, timeseries.Float64, ds.Results[0].SeriesList[0].Header.ValueFieldsList[0].DataType)
	})
	t.Run("times are exact", func(t *testing.T) {
		ds := decode(t, series(`{"name":"c","columns":["time","v"],"values":[[1577836800123456789,1]]}`))
		require.Equal(t, epoch.Epoch(1577836800123456789), ds.Results[0].SeriesList[0].Points()[0].Epoch)
	})
	t.Run("series with one header are one series", func(t *testing.T) {
		ds := decode(t, series(`{"name":"c","columns":["time","v"],"values":[[`+t1+`,2]]},`+
			`{"name":"c","columns":["time","v"],"values":[[`+t0+`,1]]}`))
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Equal(t, 2, ds.Results[0].SeriesList[0].PointCount())
	})
	t.Run("results with one statement ID are one result", func(t *testing.T) {
		ds := decode(t, `{"results":[{"statement_id":0,"series":[{"name":"a","columns":["time","v"],"values":[[`+t0+
			`,1]]}]},{"statement_id":0,"error":"e","series":[{"name":"b","columns":["time","v"],"values":[[`+t0+`,1]]}]}]}`)
		require.Len(t, ds.Results, 1)
		require.Len(t, ds.Results[0].SeriesList, 2)
		require.Equal(t, "e", ds.Results[0].Error)
	})
	t.Run("a short row's missing values are null", func(t *testing.T) {
		ds := decode(t, series(`{"name":"c","columns":["time","a","b"],"values":[[`+t0+`,1]]}`))
		require.Equal(t, []any{int64(1), nil}, ds.Results[0].SeriesList[0].Points()[0].Values)
	})
	// the legacy decoder read the whole document before it failed, crashed, or ignored what followed
	rejected := map[string]string{
		"trailing data":        `{"results":[]} {}`,
		"repeated results":     `{"results":[],"results":[]}`,
		"repeated error":       `{"results":[],"error":"a","error":"b"}`,
		"repeated id":          `{"results":[{"statement_id":0,"statement_id":1}]}`,
		"repeated series":      `{"results":[{"statement_id":0,"series":[],"series":[]}]}`,
		"repeated result err":  `{"results":[{"statement_id":0,"error":"a","error":"b"}]}`,
		"repeated name":        series(`{"name":"a","name":"b","columns":["time","v"],"values":[]}`),
		"repeated columns":     series(`{"columns":["time","v"],"columns":["time","v"],"values":[]}`),
		"repeated values":      series(`{"columns":["time","v"],"values":[],"values":[]}`),
		"repeated partial":     series(`{"columns":["time","v"],"values":[],"partial":true,"partial":true}`),
		"name after values":    series(`{"columns":["time","v"],"values":[[` + t0 + `,1]],"name":"a"}`),
		"tags after values":    series(`{"columns":["time","v"],"values":[[` + t0 + `,1]],"tags":{}}`),
		"no time column":       series(`{"name":"cpu","columns":["a","b"],"values":[]}`),
		"null result":          `{"results":[null]}`,
		"null series element":  series(`null`),
		"null row":             series(`{"name":"c","columns":["time","v"],"values":[null]}`),
		"empty row":            series(`{"name":"c","columns":["time","v"],"values":[[]]}`),
		"row without its time": series(`{"name":"c","columns":["v","time"],"values":[[1]]}`),
		"row past its columns": series(`{"name":"c","columns":["time","v"],"values":[[` + t0 + `,1,2]]}`),
	}
	for name, body := range rejected {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), WantErr: timeseries.ErrInvalidBody,
			})
		})
	}
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	// a decoded DataSet must not change when the buffer it was decoded from is reused
	for _, body := range [][]byte{valuesBody(20, 30), []byte(legacyBodies["values first"]),
		[]byte(legacyBodies["id after"]), []byte(legacyBodies["escapes"])} {
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

// valuesBody writes series of points as InfluxDB writes a GROUP BY response's float, int and string fields
func valuesBody(series, points int) []byte {
	var b strings.Builder
	b.WriteString(`{"results":[{"statement_id":0,"series":[`)
	for i := range series {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"cpu","tags":{"host":"host-` + strconv.Itoa(i) +
			`"},"columns":["time","usage","count","state"],"values":[`)
		for j := range points {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteString("[" + strconv.FormatInt(int64(1577836800+15*j)*1e9, 10) + ",")
			b.WriteString(strconv.FormatFloat(float64((i*j)%50)/4, 'f', -1, 64) + ",")
			b.WriteString(strconv.Itoa(i+j) + `,"`)
			if j%3 == 0 {
				b.WriteString(`idle"]`)
			} else {
				b.WriteString(`busy"]`)
			}
		}
		b.WriteString("]}")
	}
	b.WriteString("]}]}")
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
		body := valuesBody(shape.series, shape.points)
		b.Run(shape.name+"/legacy", func(b *testing.B) { streamtest.Bench(b, legacyUnmarshalTimeseriesReader, trq, body) })
		b.Run(shape.name+"/stream", func(b *testing.B) {
			streamtest.Bench(b, stream.ReaderUnmarshaler(newDecoder), trq, body)
		})
	}
}

// BenchmarkMarshalDecoded marshals one response as each decoder built it: the legacy decoder's
// float64s, and the stream decoder's numbers by literal, whose whole floats make a column Mixed
func BenchmarkMarshalDecoded(b *testing.B) {
	body := valuesBody(100, 1000)
	legacy, err := legacyUnmarshalTimeseriesReader(bytes.NewReader(body), decoderTRQ())
	require.NoError(b, err)
	streamed, err := UnmarshalTimeseries(body, decoderTRQ())
	require.NoError(b, err)
	for _, tf := range []struct {
		name string
		code byte
	}{{"rfc3339", 0}, {"epoch_ms", 3}} {
		rlo := &timeseries.RequestOptions{TimeFormat: tf.code}
		for name, ts := range map[string]timeseries.Timeseries{"legacy": legacy, "stream": streamed} {
			b.Run(tf.name+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := MarshalTimeseriesWriter(ts, rlo, 200, io.Discard); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
