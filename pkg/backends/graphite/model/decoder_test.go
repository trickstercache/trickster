/*
 * Copyright 2026 The Trickster Authors
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
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// a JSON escape's backslash, which these tests build their escapes from
var bs = string(rune(92))

// noteRecorder is a query's parsed form that records the step notes a decode makes
type noteRecorder struct {
	notes []string
}

func (n *noteRecorder) NoteAmbiguousStep(name string, step time.Duration) {
	n.notes = append(n.notes, fmt.Sprintf("ambiguous %q %v", name, step))
}

func (n *noteRecorder) NoteStepMismatch(target string, predicted, observed time.Duration) {
	n.notes = append(n.notes, fmt.Sprintf("mismatch %q %v %v", target, predicted, observed))
}

func noteTRQ(step time.Duration) *timeseries.TimeRangeQuery {
	q := trq(step)
	q.ParsedQuery = &noteRecorder{}
	return q
}

type graphiteCase struct {
	body string
	step time.Duration
}

// the bodies the stream decoder must decode as the decoder it replaced did; each is decoded many times
// with one query, so each predicts its step, which a decode adopts when the query has none
var graphiteBodies = map[string]graphiteCase{
	"json 60s":     {sample60s, 10 * time.Second},
	"json nulls":   {sampleNulls, 10 * time.Second},
	"raw":          {sampleRaw, 10 * time.Second},
	"raw stepped":  {sampleRaw, 10 * time.Second},
	"empty":        {"", 10 * time.Second},
	"white space":  {" \n\t\v\f\r ", 10 * time.Second},
	"wide space":   {"\xc2\xa0\u0085 ", 10 * time.Second},
	"spaced json":  {"\v\xc2\xa0 " + sample60s + "\n", 10 * time.Second},
	"spaced raw":   {"\xc2\xa0\r\n" + sampleRaw + "\r\n\n", 10 * time.Second},
	"no series":    {"[]", 0},
	"null series":  {"[null]", 0},
	"empty series": {`[{"target":"a","datapoints":[]},{"target":"b","tags":{},"datapoints":null}]`, 0},
	"members in any order": {
		`[{"datapoints":[[1,10],[2,20]],"tags":{"x":"y"},"target":"a","extra":[1,{"b":2}]}]`,
		10 * time.Second,
	},
	"members ignore case": {
		`[{"TARGET":"a","Target":"b","Tags":{"x":"1"},"TAGS":{"y":"2"},"DataPoints":[[1,10],[2,20]]}]`,
		10 * time.Second,
	},
	"members repeat": {`[{"target":"a","target":null,"tags":{"x":"1"},"tags":null,"tags":{"y":null,"y2":"2","y2":"3"},` +
		`"datapoints":[[9,0],[9,5]],"datapoints":[[1,10],[2,20]]}]`, 10 * time.Second},
	"tags null": {`[{"target":"a","tags":{"x":"1"},"tags":null,"datapoints":[[1,10],[2,20]]}]`, 10 * time.Second},
	"escapes": {`[{"target":"a` + bs + `u00e9` + bs + `"b` + bs + `/","tags":{"k` + bs + `n":"v` + bs + `ud834` + bs + `udd1e"},` +
		`"datapoints":[[1,10],[2,20]]}]`, 10 * time.Second},
	"duplicate series": {
		`[{"target":"a","datapoints":[[1,10],[2,20]]},{"target":"a","datapoints":[[3,10],[4,20]]}]`,
		10 * time.Second,
	},
	"datapoint forms": {`[{"target":"a","datapoints":[ [ 1.5 , 10.9 ] , [null,20], [-0,30], [1e308,40], [-2.5E-3,50], ` +
		`[1e-400,60], [7,70.0]]}]`, 10 * time.Second},
	"raw forms": {
		"a,b,100,130,10|1,None,3\n\n  c,100,100,10|  \nd,100,110,10|-0,1e308\r\ne,100,110,10|None\n",
		10 * time.Second,
	},
	"raw duplicates":  {"a,100,120,10|1,2\na,100,120,10|3,4\n", 10 * time.Second},
	"raw mixed steps": {"a,100,120,10|1,2\nb,100,160,60|3\n", 10 * time.Second},
}

func graphiteConformance(t *testing.T, c graphiteCase, wantErr error) {
	t.Helper()
	streamtest.Conformance(t, newDecoder, streamtest.Case{
		TRQ: trq(c.step), Body: []byte(c.body), Legacy: legacyUnmarshalTimeseriesReader, WantErr: wantErr,
	})
}

func TestDecoderMatchesLegacy(t *testing.T) {
	for name, c := range graphiteBodies {
		t.Run(name, func(t *testing.T) { graphiteConformance(t, c, nil) })
	}
}

// randomGraphite returns a JSON or raw response of random series, spaced at step from a random start
func randomGraphite(rng *weaktest.Rand, step time.Duration) string {
	secs := int64(step / time.Second)
	var b strings.Builder
	raw := rng.IntN(3) == 0
	if !raw {
		b.WriteByte('[')
	}
	for s := range 1 + rng.IntN(4) {
		start := 1787349960 + int64(rng.IntN(10))*secs
		n := 2 + rng.IntN(30)
		value := func() string {
			switch rng.IntN(5) {
			case 0:
				if raw {
					return rawNone
				}
				return "null"
			case 1:
				return strconv.Itoa(rng.IntN(1000) - 500)
			}
			return strconv.FormatFloat(rng.NormFloat64()*1e3, 'g', -1, 64)
		}
		name := "dev.s" + strconv.Itoa(rng.IntN(3))
		if raw {
			fmt.Fprintf(&b, "%s,%d,%d,%d|", name, start, start+int64(n)*secs, secs)
			for i := range n {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(value())
			}
			b.WriteByte('\n')
			continue
		}
		if s > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, `{"target": %q, `, name)
		if rng.IntN(2) == 0 {
			fmt.Fprintf(&b, `"tags": {"name": %q, "dc": "dc%d"}, `, name, rng.IntN(2))
		}
		b.WriteString(`"datapoints": [`)
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "[%s, %d]", value(), start+int64(i)*secs)
		}
		b.WriteString("]}")
	}
	if !raw {
		b.WriteByte(']')
	}
	return b.String()
}

func TestDecoderMatchesLegacyAtScale(t *testing.T) {
	rng := weaktest.NewRand(19, 19)
	for trial := range 60 {
		step := []time.Duration{10 * time.Second, time.Minute}[rng.IntN(2)]
		c := graphiteCase{randomGraphite(rng, step), step}
		t.Run(strconv.Itoa(trial), func(t *testing.T) { graphiteConformance(t, c, nil) })
	}
}

func TestDecoderErrors(t *testing.T) {
	for name, body := range map[string]string{
		"series not object":   `[1]`,
		"target not text":     `[{"target":1}]`,
		"tag not text":        `[{"target":"a","tags":{"x":1}}]`,
		"tags not object":     `[{"target":"a","tags":[1]}]`,
		"datapoints object":   `[{"target":"a","datapoints":{}}]`,
		"null datapoint":      `[{"target":"a","datapoints":[null]}]`,
		"short datapoint":     `[{"target":"a","datapoints":[[1]]}]`,
		"long datapoint":      `[{"target":"a","datapoints":[[1,2,3]]}]`,
		"text value":          `[{"target":"a","datapoints":[["1",2]]}]`,
		"null time":           `[{"target":"a","datapoints":[[1,null]]}]`,
		"huge value":          `[{"target":"a","datapoints":[[1e400,2]]}]`,
		"nested datapoint":    `[{"target":"a","datapoints":[[[1],2]]}]`,
		"cut":                 `[{"target":"a"`,
		"trailing data":       `[] x`,
		"json null":           `null`,
		"json object":         `{}`,
		"raw no pipe":         "a,1,2,3\n",
		"raw few numbers":     "a,1|1\n",
		"raw bad number":      "a,x,2,10|1\n",
		"raw zero step":       "a,1,2,0|1\n",
		"raw bad value":       "a,1,2,10|x\n",
		"raw spaced value":    "a,1,2,10|1, 2\n",
		"raw trailing comma":  "a,1,2,10|1,\n",
		"raw after good line": sampleRaw + "bad\n",
		"cut character":       "\xc2",
		"syntax after a violation": `[{"target":"a","datapoints":[[1,10],[2,30],[3,40]]},` +
			`{"target":"b","datapoints":[[1,10],[2,20]]}, x]`,
	} {
		t.Run(name, func(t *testing.T) { graphiteConformance(t, graphiteCase{body, 10 * time.Second}, streamtest.ErrAny) })
	}
	_, err := UnmarshalTimeseries([]byte(sampleJSON), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
}

// the bodies whose step checks the decoders must apply alike: the notes made, the step adopted, and
// the error, under every prediction
var stepBodies = []string{
	sampleJSON, sample60s, sampleNulls, sampleRaw, "", "[]", "[null]",
	`[{"target":"a","datapoints":[[1,100],[2,110],[3,125]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,110],[3,110]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,90]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,110],[3,120],[4,140]]}]`,
	`[{"target":"a","datapoints":[[1,100]]},{"target":"b","datapoints":[[1,100],[2,110]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,110]]},{"target":"b","datapoints":[[1,100],[2,160]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,160]]},{"target":"b","datapoints":[[1,100],[2,110],[3,125]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,110],[3,125]]},{"target":"b","datapoints":[[1,100]]}]`,
	`[{"target":"a","datapoints":[[1,100],[2,110],[3,125]]},{"target":"b","datapoints":[[1,100],[2,110]]}]`,
	`[{"target":"a","datapoints":{}}]`, `[{"target":"a","datapoints":1}]`,
	`[{"target":"a","datapoints":[[1,100],[2,110]]},{"target":"b","datapoints":[[1,100],[2,160]]}, x]`,
	"a,100,120,10|1,2\nb,100,160,60|3\n",
	"a,100,160,60|1\nb,100,120,10|1,2\nc,100,120,30|1,2\n",
	"a,100,120,10|1,2\nb,100,120,10|1,2\nbad\n",
}

func TestDecoderStepChecks(t *testing.T) {
	for _, body := range stepBodies {
		for _, step := range []time.Duration{0, 10 * time.Second, time.Minute} {
			wantQ, gotQ := noteTRQ(step), noteTRQ(step)
			want, werr := legacyUnmarshalTimeseriesReader(strings.NewReader(body), wantQ)
			got, err := UnmarshalTimeseries([]byte(body), gotQ)
			name := fmt.Sprintf("%.60q at %v", body, step)
			require.Equal(t, werr == nil, err == nil, "%s: %v / %v", name, werr, err)
			for _, target := range []error{ErrStepMismatch, ErrStepAmbiguous} {
				require.Equal(t, errors.Is(werr, target), errors.Is(err, target), "%s: %v / %v", name, werr, err)
			}
			// a step check's error is the same, where a syntax error's type is the JSON decoder's own
			if errors.Is(werr, ErrStepMismatch) || errors.Is(werr, ErrStepAmbiguous) || werr == timeseries.ErrInvalidBody {
				require.Equal(t, werr, err, name)
			}
			require.Equal(t, wantQ.ParsedQuery, gotQ.ParsedQuery, name)
			require.Equal(t, wantQ.Step, gotQ.Step, name)
			if werr == nil {
				require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), got.(*dataset.DataSet),
					streamtest.CompareOptions{IgnoreSizes: true}), name)
			}
		}
	}
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	rng := weaktest.NewRand(20, 20)
	for range 20 {
		body := []byte(randomGraphite(rng, 10*time.Second))
		want, err := UnmarshalTimeseries(body, trq(10*time.Second))
		require.NoError(t, err)
		got, err := UnmarshalTimeseries(bytes.Clone(body), trq(10*time.Second))
		require.NoError(t, err)
		for i := range body {
			body[i] = 'x'
		}
		require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), got.(*dataset.DataSet), streamtest.CompareOptions{}))
	}
}

// the reader passed to ReadFrom a byte at a time, as the sniffer reads before it knows the format
func TestDecoderReadsInPieces(t *testing.T) {
	for _, body := range []string{"  \xc2\xa0" + sample60s, "\xc2\xa0" + sampleRaw, " \n ", "\xc2"} {
		want, werr := UnmarshalTimeseries([]byte(body), trq(10*time.Second))
		got, err := UnmarshalTimeseriesReader(io.MultiReader(strings.NewReader(body[:1]), strings.NewReader(body[1:])),
			trq(10*time.Second))
		require.Equal(t, werr == nil, err == nil, "%q", body)
		if werr == nil {
			require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), got.(*dataset.DataSet), streamtest.CompareOptions{}))
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	// as a float, truncated, which an integer of up to 15 digits is exactly
	for _, in := range []string{
		"0", "-0", "7", "-7", "007", "-007", "1787349960", "123456789012345", "-123456789012345",
		"1234567890123456", "9007199254740993", "99999999999999999999", "10.9", "-10.9", "1e3", "1E-3", "-1.5e2",
	} {
		f, err := strconv.ParseFloat(in, 64)
		require.NoError(t, err)
		got, err := parseTimestamp([]byte(in))
		require.NoError(t, err)
		require.Equal(t, int64(f), got, in)
	}
	for _, in := range []string{"", "-", "x", "1x", "null"} {
		_, err := parseTimestamp([]byte(in))
		require.Error(t, err, in)
	}
}

func BenchmarkDecoder(b *testing.B) {
	for _, shape := range []struct{ series, points int }{{10, 10000}, {1000, 100}} {
		var sb strings.Builder
		sb.WriteByte('[')
		for s := range shape.series {
			if s > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, `{"target": "dev.s%d", "tags": {"name": "dev.s%d"}, "datapoints": [`, s, s)
			for i := range shape.points {
				if i > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "[%s, %d]", strconv.FormatFloat(float64(s*i%997)/7, 'g', -1, 64), 1787000000+int64(i)*10)
			}
			sb.WriteString("]}")
		}
		sb.WriteByte(']')
		body := []byte(sb.String())
		q := trq(10 * time.Second)
		name := fmt.Sprintf("%dx%d", shape.series, shape.points)
		b.Run(name+"/legacy", func(b *testing.B) { streamtest.Bench(b, legacyUnmarshalTimeseriesReader, q, body) })
		b.Run(name+"/stream", func(b *testing.B) { streamtest.Bench(b, UnmarshalTimeseriesReader, q, body) })
	}
}
