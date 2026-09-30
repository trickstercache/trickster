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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/stretchr/testify/require"
)

func decoderTRQ() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: "up",
		Extent:    timeseries.Extent{Start: time.Unix(1435781400, 0), End: time.Unix(1435781500, 0)},
		Step:      15 * time.Second,
	}
}

// bodies the stream decoder must decode exactly as the decoder it replaced did
var legacyBodies = map[string]string{
	"matrix":           testMatrix,
	"vector":           testVector,
	"vector2":          testVector2,
	"histogram matrix": testHistogramMatrix,
	"mixed matrix":     testMixedMatrix,
	"histogram vector": testHistogramVector,
	"scalar":           `{"status":"success","data":{"resultType":"scalar","result":[1435781430,"1"]}}`,
	"scalar empty":     `{"status":"success","data":{"resultType":"scalar","result":[]}}`,
	"scalar one":       `{"status":"success","data":{"resultType":"scalar","result":[1435781430]}}`,
	"scalar three":     `{"status":"success","data":{"resultType":"scalar","result":[1435781430,"1","2"]}}`,
	"scalar null":      `{"status":"success","data":{"resultType":"scalar","result":null}}`,
	"scalar missing":   `{"status":"success","data":{"resultType":"scalar"}}`,
	"matrix empty":     `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
	"matrix null":      `{"status":"success","data":{"resultType":"matrix","result":null}}`,
	"matrix missing":   `{"status":"success","data":{"resultType":"matrix"}}`,
	"vector empty":     `{"status":"success","data":{"resultType":"vector","result":[]}}`,
	"vector no samples": `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"a":"b"}},` +
		`{"metric":{"a":"c"},"value":[],"histogram":[1435781430,{"count":"1"}]},{"metric":{"a":"d"},"value":null},` +
		`{"metric":{"a":"e"},"histogram":[1435781430]}]}}`,
	"no data":       `{"status":"error","errorType":"bad_data","error":"invalid parameter"}`,
	"data null":     `{"status":"success","data":null}`,
	"document null": `null`,
	"string type":   `{"status":"success","data":{"resultType":"string","result":[1435781430,"x"]}}`,
	"unknown type":  `{"status":"success","data":{"resultType":"other","result":"anything"}}`,
	"no type":       `{"status":"success","data":{"result":[]}}`,
	"envelope": `{"status":"success","warnings":["w1","w2"],"infos":["i"],"stats":{"x":1},` +
		`"data":{"resultType":"matrix","result":[{"metric":{"a":"b"},"values":[[1435781430,"1"]]}]}}`,
	"type after result": `{"status":"success","data":{"result":[{"metric":{"__name__":"up","a":"b"},` +
		`"values":[[1435781430,"1"],[1435781445,"2"]]}],"resultType":"matrix"}}`,
	"vector type after result": `{"status":"success","data":{"result":[{"metric":{"a":"b"},` +
		`"value":[1435781430,"1"]},{"metric":{"a":"c"},"value":[1435781445,"2"]}],"resultType":"vector"}}`,
	"scalar type after result": `{"status":"success","data":{"result":[1435781430,"7"],"resultType":"scalar"}}`,
	"empty type after result":  `{"status":"success","data":{"result":[],"resultType":"scalar"}}`,
	"string type after result": `{"status":"success","data":{"result":[{"metric":{}}],"resultType":"string"}}`,
	"metric after values": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"values":[[1435781430,"1"]],"histograms":[[1435781430,{"count":"1"}]],"metric":{"a":"b"}}]}}`,
	"metric between": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"values":[[1435781430,"1"]],"metric":{"a":"b"},"histograms":[[1435781430,{"count":"1"}]]}]}}`,
	"no metric": `{"status":"success","data":{"resultType":"matrix","result":[{"values":[[1435781430,"1"]]}]}}`,
	"histograms, no metric": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"histograms":[[1435781430,{"count":"1"}]]}]}}`,
	"extras": `{"status":"success","data":{"resultType":"matrix","stats":{"a":1},"result":[` +
		`{"metric":{"a":"b"},"extra":[1],"values":null,"histograms":null}]}}`,
	"strings before type": `{"status":"success","data":{"result":["a",{"b":1}],"resultType":"string"}}`,
	"null metric": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":null,"values":[[1435781430,"1"]]}]}}`,
	"no samples": `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"a":"b"}}]}}`,
	"null label": `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"a":null}}]}}`,
	"empty values with histograms": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"a":"b"},"values":[],"histograms":[[1435781430,{"count":"1"}]]}]}}`,
	"empty histograms": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"a":"b"},"histograms":[]}]}}`,
	"invalid histograms only": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"a":"b"},"histograms":[["bad",{}]]}]}}`,
	"case": `{"Status":"success","Data":{"ResultType":"matrix","Result":[` +
		`{"Metric":{"A":"b"},"VALUES":[[1435781430,"1"]]}]}}`,
	"other type's fields": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"a":"b"},"value":[1435781430,"1"],"values":[[1435781445,"2"]]}]}}`,
	"vector ignores matrix fields": `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"a":"b"},"value":[1435781430,"1"],"values":[[1435781445,"2"]]}]}}`,
	"dropped samples": `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"a":"b"},"values":[` +
		`[1435781430,"1"],["bad","x"],[1435781445,2],[1435781450],[1435781455,"3","4"],null,[0,"5"],[-5,"6"],` +
		`[1435781460,"7"]]}]}}`,
	"escapes": `{"status":"success","data":{"resultType":"matrix","result":[{"metric":` +
		`{"a\"b":"c\\dé\n","e":"😀"},"values":[[1435781430,"10"]]}]}}`,
	"spacing": "{ \"status\" : \"success\" ,\n \"data\" : { \"resultType\" : \"matrix\" , \"result\" : [ " +
		"{ \"metric\" : { \"a\" : \"b\" } , \"values\" : [ [ 1435781430 , \"1\" ] , [ 1435781445 , \"2\" ] ] } ] } }\n",
	"exponent time": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"a":"b"},"values":[[1.43578143e9,"1"],[1435781445.0,"2"]]}]}}`,
	"histogram shapes": `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"a":"b"},` +
		`"histograms":[[1435781430,{"sum":"1e3","count":"10","zero_threshold":2.938735877055719e-39,` +
		`"schema":-4,"buckets":[[0,"-0.5","0.5","1"],[3,"0.5","1e+21","2"]],"custom_values":[1.0,2.5,1e21,` +
		`123456789012345678],"n":-0,"s":"<a&b>","u":" é","d":1,"d":2,"z":{"b":1,"a":[true,false,null]}}]]}]}}`,
	"histogram not an object": `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"a":"b"},"histograms":[[1435781430,"text"],[1435781445,[1,2]]]}]}}`,
}

func TestDecoderMatchesLegacy(t *testing.T) {
	for name, body := range legacyBodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), Legacy: legacyUnmarshalTimeseriesReader,
			})
		})
	}
}

func TestDecoderMatchesLegacyAtScale(t *testing.T) {
	for _, shape := range []struct{ series, points int }{{50, 200}, {2000, 3}} {
		streamtest.Conformance(t, newDecoder, streamtest.Case{
			TRQ: decoderTRQ(), Body: matrixBody(shape.series, shape.points, false),
			Legacy: legacyUnmarshalTimeseriesReader,
		})
		streamtest.Conformance(t, newDecoder, streamtest.Case{
			TRQ: decoderTRQ(), Body: matrixBody(shape.series, shape.points, true),
			Legacy: legacyUnmarshalTimeseriesReader,
		})
	}
}

func TestDecoderErrors(t *testing.T) {
	bodies := map[string]string{
		"invalid":                `{sta`,
		"truncated":              `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{}`,
		"trailing":               `{"status":"success"} {}`,
		"not an object":          `[1]`,
		"matrix not an array":    `{"data":{"resultType":"matrix","result":"x"}}`,
		"matrix of numbers":      `{"data":{"resultType":"matrix","result":[1,"2"]}}`,
		"matrix of strings":      `{"data":{"resultType":"matrix","result":["a"]}}`,
		"matrix mixed elements":  `{"data":{"resultType":"matrix","result":[{},1]}}`,
		"scalar not an array":    `{"data":{"resultType":"scalar","result":"x"}}`,
		"values not an array":    `{"data":{"resultType":"matrix","result":[{"values":"x"}]}}`,
		"sample not an array":    `{"data":{"resultType":"matrix","result":[{"values":[5]}]}}`,
		"value not an array":     `{"data":{"resultType":"vector","result":[{"value":"x"}]}}`,
		"metric not an object":   `{"data":{"resultType":"matrix","result":[{"metric":[]}]}}`,
		"label not a string":     `{"data":{"resultType":"matrix","result":[{"metric":{"a":1}}]}}`,
		"status not a string":    `{"status":1}`,
		"type not a string":      `{"data":{"resultType":1}}`,
		"repeated values":        `{"data":{"resultType":"matrix","result":[{"values":[],"values":[]}]}}`,
		"repeated result":        `{"data":{"resultType":"matrix","result":[],"result":[]}}`,
		"repeated type":          `{"data":{"resultType":"matrix","resultType":"matrix"}}`,
		"repeated data":          `{"data":{},"data":{}}`,
		"late matrix type":       `{"data":{"result":[1435781430,"1"],"resultType":"matrix"}}`,
		"late scalar type":       `{"data":{"result":[{"metric":{}}],"resultType":"scalar"}}`,
		"late vector type":       `{"data":{"result":[{"values":[]}],"resultType":"vector"}}`,
		"late matrix of vectors": `{"data":{"result":[{"value":[1,"1"]}],"resultType":"matrix"}}`,
		"late type, other shape": `{"data":{"result":"x","resultType":"matrix"}}`,
		"histogram overflow":     `{"data":{"resultType":"matrix","result":[{"histograms":[[1,{"a":1e400}]]}]}}`,
		"time overflow":          `{"data":{"resultType":"matrix","result":[{"values":[[1e400,"1"]]}]}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: decoderTRQ(), Body: []byte(body), WantErr: streamtest.ErrAny,
			})
		})
	}
	_, err := UnmarshalTimeseries([]byte(testMatrix), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	_, err = UnmarshalTimeseriesReader(nil, nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	_, err = UnmarshalTimeseriesReader(nil, decoderTRQ())
	require.Error(t, err)
	_, err = newDecoder(nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
}

// decode returns body's DataSet, failing the test on an error
func decode(t *testing.T, body string) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseries([]byte(body), decoderTRQ())
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func TestDecoderDepartures(t *testing.T) {
	t.Run("sub-second times are exact", func(t *testing.T) {
		ds := decode(t, `{"status":"success","data":{"resultType":"matrix","result":[`+
			`{"metric":{"a":"b"},"values":[[1435781430.781,"1"],[1435781430.1,"2"]]}]}}`)
		pts := ds.Results[0].SeriesList[0].Points()
		require.Equal(t, epoch.Epoch(1435781430100000000), pts[0].Epoch)
		require.Equal(t, epoch.Epoch(1435781430781000000), pts[1].Epoch, "and sorted")
		b, err := MarshalTimeseries(ds, nil, 200)
		require.NoError(t, err)
		require.Contains(t, string(b), `[[1435781430.1,"2"],[1435781430.781,"1"]]`)
	})
	t.Run("a repeated label set is one series", func(t *testing.T) {
		ds := decode(t, `{"status":"success","data":{"resultType":"matrix","result":[`+
			`{"metric":{"a":"b"},"values":[[1435781430,"1"]]},{"metric":{"a":"b"},"values":[[1435781445,"2"]]}]}}`)
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Equal(t, 2, ds.Results[0].SeriesList[0].PointCount())
	})
	t.Run("an invalid vector sample is dropped", func(t *testing.T) {
		ds := decode(t, `{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"a":"b"},"value":[1435781430,1]}]}}`)
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Zero(t, ds.Results[0].SeriesList[0].PointCount())
		require.Equal(t, decoderTRQ().Extent, ds.ExtentList[0])
	})
	t.Run("an invalid scalar is dropped", func(t *testing.T) {
		ds := decode(t, `{"status":"success","data":{"resultType":"scalar","result":[{},{}]}}`)
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Zero(t, ds.Results[0].SeriesList[0].PointCount())
		require.Equal(t, decoderTRQ().Extent, ds.ExtentList[0])
	})
	t.Run("vector samples keep any time", func(t *testing.T) {
		ds := decode(t, `{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"a":"b"},"value":[0,"1"]}]}}`)
		require.Equal(t, 1, ds.Results[0].SeriesList[0].PointCount())
		require.Equal(t, time.Unix(0, 0), ds.ExtentList[0].Start)
	})
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	// a decoded DataSet must not change when the buffer it was decoded from is reused
	body := matrixBody(20, 30, true)
	pristine := bytes.Clone(body)
	ts, err := UnmarshalTimeseries(body, decoderTRQ())
	require.NoError(t, err)
	for i := range body {
		body[i] = 'x'
	}
	want, err := UnmarshalTimeseries(pristine, decoderTRQ())
	require.NoError(t, err)
	require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), ts.(*dataset.DataSet), streamtest.CompareOptions{}))
}

func TestPairOf(t *testing.T) {
	for raw, want := range map[string][3]string{
		`[]`:                {"", "", "0"},
		`[ ]`:               {"", "", "0"},
		`null`:              {"", "", "0"},
		`[1]`:               {"1", "", "1"},
		`[1,"a"]`:           {"1", `"a"`, "2"},
		`[ 1 , "a" ]`:       {"1", `"a"`, "2"},
		`[1,{"a":[1,"]"]}]`: {"1", `{"a":[1,"]"]}`, "2"},
		`["\"]",2,3]`:       {`"\"]"`, "2", "3"},
		`[[1,2],[3],[]]`:    {"[1,2]", "[3]", "3"},
	} {
		first, second, n := pairOf([]byte(raw))
		require.Equal(t, want, [3]string{string(first), string(second), strconv.Itoa(n)}, raw)
	}
}

// matrixBody returns a matrix of series with points each, of values or of histograms
func matrixBody(series, points int, histograms bool) []byte {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := range series {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metric":{"__name__":"up","job":"node","instance":"host-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`:9100"},"`)
		if histograms {
			b.WriteString(`histograms":[`)
		} else {
			b.WriteString(`values":[`)
		}
		for j := range points {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('[')
			b.WriteString(strconv.FormatInt(1435781400+int64(j)*15, 10))
			if histograms {
				b.WriteString(`,{"count":"`)
				b.WriteString(strconv.Itoa(i + j))
				b.WriteString(`","sum":"`)
				b.WriteString(strconv.FormatFloat(float64(i*j)/7, 'f', -1, 64))
				b.WriteString(`","buckets":[[0,"-0.5","0.5","1"],[0,"0.5","1.5","`)
				b.WriteString(strconv.Itoa(j))
				b.WriteString(`"]]}]`)
				continue
			}
			b.WriteString(`,"`)
			b.WriteString(strconv.FormatFloat(float64((i*j)%977)/7, 'f', -1, 64))
			b.WriteString(`"]`)
		}
		b.WriteString("]}")
	}
	b.WriteString("]}}")
	return []byte(b.String())
}

func BenchmarkDecoder(b *testing.B) {
	trq := decoderTRQ()
	for _, shape := range []struct {
		name           string
		series, points int
		histograms     bool
	}{
		{"matrix-100x1000", 100, 1000, false},
		{"matrix-10000x10", 10000, 10, false},
		{"histograms-100x100", 100, 100, true},
	} {
		body := matrixBody(shape.series, shape.points, shape.histograms)
		b.Run(shape.name+"/legacy", func(b *testing.B) { streamtest.Bench(b, legacyUnmarshalTimeseriesReader, trq, body) })
		b.Run(shape.name+"/stream", func(b *testing.B) {
			streamtest.Bench(b, stream.ReaderUnmarshaler(newDecoder), trq, body)
		})
	}
}
