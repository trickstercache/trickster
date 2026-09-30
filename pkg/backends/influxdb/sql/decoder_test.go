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

package sql

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
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func decoderTRQ() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: "SELECT date_bin(INTERVAL '1 minute', time) AS time, region, host, avg(usage) FROM cpu",
		Extent: timeseries.Extent{Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			End: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)},
		Step:                time.Minute,
		TimestampDefinition: timeseries.FieldDefinition{Name: "time", Role: timeseries.RoleTimestamp},
		// listed out of the columns' order, which orders the series
		TagFieldDefintions: timeseries.FieldDefinitions{
			{Name: "region", Role: timeseries.RoleTag}, {Name: "host", Role: timeseries.RoleTag},
		},
	}
}

// bodies the stream decoder must decode exactly as the decoder it replaced did
var legacyBodies = map[string]string{
	"json grouped": `[{"time":"2024-01-01T00:00:00","host":"b","region":"us","usage":1.5,"n":1},` +
		`{"time":"2024-01-01T00:00:00","host":"a","region":"us","usage":2,"n":2},` +
		`{"time":"2024-01-01T00:01:00","host":"b","region":"eu","usage":3.25,"n":3},` +
		`{"time":"2024-01-01T00:01:00","host":"a","region":"us","usage":4,"n":4}]`,
	"json types": `[{"time":"2024-01-01T00:00:00","i":1,"f":1.5,"s":"x","b":true,"ns":"5","fs":"2.5","bs":"true","o":{"a":[1,2.5]},"a":[1,"x"],"z":null},` +
		`{"time":"2024-01-01T00:01:00","i":2.5,"f":3,"s":7,"b":1,"ns":"x","fs":"Inf","bs":"1","o":"t","a":{"k":null},"z":"q"},` +
		`{"time":"2024-01-01T00:02:00","i":"9","f":"1e3","s":false,"b":"F","ns":"-12","fs":"","bs":"maybe","o":[true],"a":3,"z":1},` +
		`{"time":"2024-01-01T00:03:00","i":1e400,"f":1e400,"s":1e400,"b":1e400,"ns":true,"fs":{"x":1},"bs":null,"o":null,"a":null,"z":false}]`,
	"json first null":   `[{"time":"2024-01-01T00:00:00","v":null,"s":""},{"time":"2024-01-01T00:01:00","v":7,"s":""},{"time":"2024-01-01T00:02:00","v":null,"s":"2"}]`,
	"json omitted":      `[{"time":"2024-01-01T00:00:00","host":"a","a":1,"b":2},{"time":"2024-01-01T00:01:00","host":"a","b":3},{"b":4,"time":"2024-01-01T00:02:00","a":5,"extra":6}]`,
	"json late time":    `[{"host":"a","v":1},{"host":"a","v":2,"time":"2024-01-01T00:01:00"}]`,
	"json no time":      `[{"host":"a","v":1},{"host":"b","v":2}]`,
	"json time forms":   `[{"time":"2024-01-01T00:00:00,25","v":0},{"time":"2024-01-01T00:00:00.5","v":1},{"time":"2024-01-01T00:00:01Z","v":2},{"time":"2024-01-01T01:00:02+01:00","v":3},{"time":"2024-01-01 00:00:03.25","v":4},{"time":1704067300,"v":6},{"time":1704067301000,"v":7},{"time":1704067302000000,"v":8},{"time":1704067303000000000,"v":9},{"time":"1704067304","v":10},{"time":1.5,"v":11},{"time":true,"v":12},{"time":null,"v":13},{"time":"x","v":14},{"time":{},"v":15},{"time":"2024-01-02","v":5}]`,
	"json tag forms":    `[{"time":"2024-01-01T00:00:00","host":null,"region":1.5,"v":1},{"time":"2024-01-01T00:00:00","host":true,"region":{"a":1},"v":2},{"time":"2024-01-01T00:00:00","region":[1],"v":3},{"time":"2024-01-01T00:00:00","host":"","region":"","v":4}]`,
	"json escapes":      `[{"time":"2024-01-01T00:00:00","hé":"é\n","v":"a\"b"},{"time":"2024-01-01T00:01:00","hé":"x","v":"😀"}]`,
	"json magnitudes":   `[{"time":-99999999999,"v":1},{"time":12345678901234,"v":2},{"time":99999999999999999,"v":3}]`,
	"json dropped type": `[{"host":"a","v":"x"},{"time":"2024-01-01T00:00:00","host":"a","v":2}]`,
	"json null rows":    `[{"time":"2024-01-01T00:00:00","v":1},null,{"time":"2024-01-01T00:01:00","v":2},null]`,
	"json empty":        `[]`,
	"json empty first":  `[{},{"time":"2024-01-01T00:00:00","v":1}]`,
	"json repeated":     `[{"time":"2024-01-01T00:00:00","v":1},{"time":"2024-01-01T00:01:00","v":2,"v":3,"time":"2024-01-01T00:02:00"}]`,
	"json duplicates":   `[{"time":"2024-01-01T00:00:00","host":"a","v":1},{"time":"2024-01-01T00:00:00","host":"a","v":2},{"time":"2024-01-01T00:01:00","host":"a","v":3}]`,
	"json whitespace":   "[ \n{ \"time\" : \"2024-01-01T00:00:00\" , \"v\" : 1 } ,\n\t{\"time\":\"2024-01-01T00:01:00\",\"v\":2}\n]\n",
	"json invalid utf8": "[{\"time\":\"2024-01-01T00:00:00\",\"host\":\"a\xffb\",\"v\":\"c\xfe\"}]",
	"jsonl": "{\"time\":\"2024-01-01T00:00:00\",\"host\":\"a\",\"region\":\"us\",\"v\":1}\n" +
		"{\"time\":\"2024-01-01T00:00:00\",\"host\":\"b\",\"region\":\"us\",\"v\":2.5}\n\n" +
		"{\"time\":\"2024-01-01T00:01:00\",\"host\":\"a\",\"region\":\"us\",\"v\":\"3\"}\n",
	"jsonl crlf":       "{\"time\":\"2024-01-01T00:00:00\",\"v\":1}\r\n{\"time\":\"2024-01-01T00:01:00\",\"v\":2}\r\n",
	"jsonl no newline": "{\"time\":\"2024-01-01T00:00:00\",\"v\":1}\n  {\"time\":\"2024-01-01T00:01:00\",\"v\":null}  ",
	"jsonl null line":  "{\"time\":\"2024-01-01T00:00:00\",\"v\":1}\nnull\n{\"time\":\"2024-01-01T00:01:00\",\"v\":2}\n",
	"csv": "time,host,region,usage,n,ok,s\n" +
		"2024-01-01T00:00:00,a,us,1.5,1,true,x\n" +
		"2024-01-01T00:00:00,b,eu,2,2,false,y\n" +
		"2024-01-01T00:01:00,a,us,,3,,\n" +
		"2024-01-01T00:01:00,b,eu,x,z,1,\n",
	"csv quoted":      "\"time\",\"host,name\",v\n\"2024-01-01T00:00:00\",\"a,b\",\"1\"\n2024-01-01T00:01:00,\"c\"\"d\",\"multi\nline\"\n",
	"csv crlf":        "time,v\r\n2024-01-01T00:00:00,1\r\n\r\n2024-01-01T00:01:00,2",
	"csv header only": "time,host,v\n",
	"csv late type":   "time,v,w\n2024-01-01T00:00:00,,\n2024-01-01T00:01:00,5,true\n2024-01-01T00:02:00,5.5,1\n",
	"csv time forms":  "time,v\n1704067200,1\n2024-01-01 00:00:01,2\nnope,3\n,4\n2024-01-01T00:00:05Z,5\n",
	"csv no time":     "t,v\n2024-01-01T00:00:00,1\n",
}

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
	rng := weaktest.NewRand(5, 5)
	for trial := range 30 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) { conformance(t, randomBody(rng, trial%3, 1+rng.IntN(200))) })
	}
}

// randomBody writes rows in time order in one of the three forms, with values of every kind
func randomBody(rng *weaktest.Rand, form, rows int) string {
	var b strings.Builder
	cols := []string{"time", "host", "region", "a", "b", "c"}
	if form == 0 {
		b.WriteByte('[')
	} else if form == 2 {
		b.WriteString(strings.Join(cols, ",") + "\n")
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range rows {
		ts := start.Add(time.Duration(i) * time.Second).Format(v3TimestampLayouts[0])
		vals := []string{ts, "h" + strconv.Itoa(rng.IntN(4)), "r" + strconv.Itoa(rng.IntN(2)),
			randomCell(rng, form), randomCell(rng, form), randomCell(rng, form)}
		switch form {
		case 2:
			b.WriteString(strings.Join(vals, ",") + "\n")
			continue
		case 0:
			if i > 0 {
				b.WriteByte(',')
			}
		}
		b.WriteByte('{')
		for c, name := range cols {
			if c > 0 {
				b.WriteByte(',')
			}
			v := vals[c]
			if c < 3 {
				v = strconv.Quote(v)
			}
			b.WriteString(strconv.Quote(name) + ":" + v)
		}
		b.WriteByte('}')
		if form == 1 {
			b.WriteByte('\n')
		}
	}
	if form == 0 {
		b.WriteByte(']')
	}
	return b.String()
}

func randomCell(rng *weaktest.Rand, form int) string {
	var v string
	switch rng.IntN(5) {
	case 0:
		v = strconv.FormatFloat(rng.Float64()*100, 'f', -1, 64)
	case 1:
		v = strconv.Itoa(rng.IntN(1000) - 500)
	case 2:
		v = strconv.FormatBool(rng.IntN(2) == 0)
	case 3:
		if form == 2 {
			return ""
		}
		return "null"
	default:
		if form == 2 {
			return "s" + strconv.Itoa(rng.IntN(5))
		}
		return strconv.Quote("s" + strconv.Itoa(rng.IntN(5)))
	}
	return v
}

func TestDecoderErrors(t *testing.T) {
	bodies := map[string]string{
		"empty":             ``,
		"json number row":   `[5]`,
		"json first null":   `[null]`,
		"json later number": `[{"time":"2024-01-01T00:00:00","v":1},5]`,
		"json later array":  `[{"time":"2024-01-01T00:00:00","v":1},[1]]`,
		"json later string": `[{"time":"2024-01-01T00:00:00","v":1},"x"]`,
		"json truncated":    `[{"time":"2024-01-01T00:00:00","v":1}`,
		"json cut in row":   `[{"time":"2024-01-01T00:00:00","v":`,
		"json bad":          `[{"time":}]`,
		"jsonl number":      "{\"time\":\"2024-01-01T00:00:00\",\"v\":1}\n5\n",
		"jsonl bad":         "{\"time\":\"2024-01-01T00:00:00\",\"v\":1}\n{not json}\n",
		"jsonl truncated":   "{\"time\":\"2024-01-01T00:00:00\",\"v\":",
		"csv field count":   "time,v\n2024-01-01T00:00:00,1,2\n",
		"csv bare quote":    "time,v\n2024-01-01T00:00:00,a\"b\n",
		"csv open quote":    "time,v\n2024-01-01T00:00:00,\"ab\n",
		"csv not json":      " [{\"a\":1}]\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), WantErr: streamtest.ErrAny,
				Legacy: legacyUnmarshalTimeseriesReader,
			})
		})
	}
	_, err := UnmarshalTimeseries([]byte(`[]`), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	_, err = UnmarshalTimeseriesReader(strings.NewReader(`[]`), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	ts, err := UnmarshalTimeseriesReader(strings.NewReader(legacyBodies["csv"]), decoderTRQ())
	require.NoError(t, err)
	require.Len(t, ts.(*dataset.DataSet).Results[0].SeriesList, 2)
}

func decode(t *testing.T, body string) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseries([]byte(body), decoderTRQ())
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func TestDecoderDepartures(t *testing.T) {
	t.Run("a series' rows are sorted", func(t *testing.T) {
		for _, body := range []string{
			`[{"time":"2024-01-01T00:02:00","v":1},{"time":"2024-01-01T00:00:00","v":2},{"time":"2024-01-01T00:01:00","v":3}]`,
			"time,v\n2024-01-01T00:02:00,1\n2024-01-01T00:00:00,2\n2024-01-01T00:01:00,3\n",
		} {
			s := decode(t, body).Results[0].SeriesList[0]
			require.True(t, s.IsSorted())
			pts := s.Points()
			require.Equal(t, []any{int64(2)}, pts[0].Values)
			require.Equal(t, []any{int64(1)}, pts[2].Values)
		}
	})
	t.Run("a repeated column is one column", func(t *testing.T) {
		for _, body := range []string{
			`[{"time":"2024-01-01T00:00:00","v":1,"v":2}]`,
			"time,v,v\n2024-01-01T00:00:00,1,2\n",
		} {
			s := decode(t, body).Results[0].SeriesList[0]
			require.Len(t, s.Header.ValueFieldsList, 1)
			require.Equal(t, []any{int64(2)}, s.Points()[0].Values)
		}
	})
	t.Run("JSON Lines are JSON values one after another", func(t *testing.T) {
		ds := decode(t, "{\"time\":\"2024-01-01T00:00:00\",\n\"v\":1}{\"time\":\"2024-01-01T00:01:00\",\"v\":2}\n")
		require.Equal(t, 2, ds.Results[0].SeriesList[0].PointCount())
	})
	rejected := map[string]string{
		"json trailing data":  `[{"time":"2024-01-01T00:00:00","v":1}] x`,
		"jsonl trailing text": "{\"time\":\"2024-01-01T00:00:00\",\"v\":1} x\n",
	}
	for name, body := range rejected {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), WantErr: streamtest.ErrAny,
			})
		})
	}
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	// a decoded DataSet must not change when the buffer it was decoded from is reused
	rng := weaktest.NewRand(6, 6)
	for form := range 3 {
		for _, body := range [][]byte{[]byte(randomBody(rng, form, 50)), rowsBody(form, 20, 5)} {
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
}

// rowsBody writes a GROUP BY host response of float, int and string values in one of the three forms
func rowsBody(form, hosts, points int) []byte {
	var b strings.Builder
	switch form {
	case 0:
		b.WriteByte('[')
	case 2:
		b.WriteString("time,host,usage,count,state\n")
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for p := range points {
		ts := start.Add(time.Duration(p) * time.Minute).Format(v3TimestampLayouts[0])
		for h := range hosts {
			usage := strconv.FormatFloat(float64((h*p)%50)/4, 'f', -1, 64)
			count, state := strconv.Itoa(h+p), "busy"
			if p%3 == 0 {
				state = "idle"
			}
			if form == 2 {
				b.WriteString(ts + ",host-" + strconv.Itoa(h) + "," + usage + "," + count + "," + state + "\n")
				continue
			}
			if form == 0 && (p > 0 || h > 0) {
				b.WriteByte(',')
			}
			b.WriteString(`{"time":"` + ts + `","host":"host-` + strconv.Itoa(h) + `","usage":` + usage +
				`,"count":` + count + `,"state":"` + state + `"}`)
			if form == 1 {
				b.WriteByte('\n')
			}
		}
	}
	if form == 0 {
		b.WriteByte(']')
	}
	return []byte(b.String())
}

func TestDecoderTimes(t *testing.T) {
	ds := decode(t, `[{"time":"2024-01-01T00:00:00.123456789","v":1}]`)
	require.Equal(t, epoch.Epoch(1704067200123456789), ds.Results[0].SeriesList[0].Points()[0].Epoch)
}

func BenchmarkDecoder(b *testing.B) {
	trq := decoderTRQ()
	for form, name := range []string{"json", "jsonl", "csv"} {
		body := rowsBody(form, 100, 1000)
		b.Run(name+"/legacy", func(b *testing.B) { streamtest.Bench(b, legacyUnmarshalTimeseriesReader, trq, body) })
		b.Run(name+"/stream", func(b *testing.B) {
			streamtest.Bench(b, stream.ReaderUnmarshaler(newDecoder), trq, body)
		})
	}
}
