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

package streamtest

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recorder) Error(args ...any) {
	r.errs = append(r.errs, fmt.Sprint(args...))
}

func (r *recorder) requireReported(t *testing.T, subs ...string) {
	t.Helper()
	require.NotEmpty(t, r.errs)
	all := strings.Join(r.errs, "\n")
	for _, sub := range subs {
		require.Contains(t, all, sub)
	}
}

var (
	testTRQ = &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(10, 0)},
	}
	csvFields = timeseries.SeriesFields{
		Tags:   timeseries.FieldDefinitions{{Name: "host", DataType: timeseries.String}},
		Values: timeseries.FieldDefinitions{{Name: "v", DataType: timeseries.String}},
	}
	errPicky = errors.New("single byte write")
	csvBody  = []byte("3,a,x\n1,b,y\n2,a,z\n1,a,w\n")
)

func csvDecoder(opts dataset.BuilderOptions) stream.NewDecoderFunc {
	opts.Fields = csvFields
	return func(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
		b := dataset.NewBuilder(trq, opts)
		var cols [][]byte
		onLine := func(line []byte) error {
			cols = stream.SplitFields(line, ',', cols)
			if len(cols) != 3 {
				return timeseries.ErrInvalidBody
			}
			ep, err := epoch.ParseDecimal(cols[0], timeseries.DateTimeUnixSecs)
			if err != nil {
				return err
			}
			r := b.Row()
			r.SetEpoch(ep)
			r.SetTag(0, cols[1])
			r.AddValue(string(cols[2]))
			return r.Commit()
		}
		return stream.NewLines(onLine, func() (timeseries.Timeseries, error) {
			return b.Finish()
		}), nil
	}
}

func wrapDecoder(newDecoder stream.NewDecoderFunc,
	wrap func(stream.Decoder) stream.Decoder,
) stream.NewDecoderFunc {
	return func(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
		dec, err := newDecoder(trq)
		if err != nil {
			return nil, err
		}
		return wrap(dec), nil
	}
}

type countingDecoder struct {
	stream.Decoder
	writes int
}

func (c *countingDecoder) Write(p []byte) (int, error) {
	c.writes++
	return c.Decoder.Write(p)
}

func (c *countingDecoder) Finish() (timeseries.Timeseries, error) {
	ts, err := c.Decoder.Finish()
	if ds, ok := ts.(*dataset.DataSet); ok {
		ds.Status = strconv.Itoa(c.writes)
	}
	return ts, err
}

type pickyDecoder struct{ stream.Decoder }

func (p pickyDecoder) Write(b []byte) (int, error) {
	if len(b) == 1 {
		return 0, errPicky
	}
	return p.Decoder.Write(b)
}

type lenientDecoder struct{ stream.Decoder }

func (l lenientDecoder) ReadFrom(r io.Reader) (int64, error) {
	b, _ := io.ReadAll(r)
	n, err := l.Write(b)
	return int64(n), err
}

type wrapped struct{ *dataset.DataSet }

type wrappingDecoder struct{ stream.Decoder }

func (w wrappingDecoder) Finish() (timeseries.Timeseries, error) {
	ts, err := w.Decoder.Finish()
	if err != nil {
		return nil, err
	}
	return wrapped{ts.(*dataset.DataSet)}, nil
}

func legacyFrom(newDecoder stream.NewDecoderFunc) timeseries.UnmarshalerReaderFunc {
	return stream.ReaderUnmarshaler(newDecoder)
}

func TestConformancePasses(t *testing.T) {
	dec := csvDecoder(dataset.BuilderOptions{SeriesName: "csv"})
	want, err := stream.BytesUnmarshaler(dec)(csvBody, testTRQ)
	require.NoError(t, err)
	c := Case{TRQ: testTRQ, Body: csvBody, Want: want.(*dataset.DataSet),
		Legacy: legacyFrom(dec), Shuffle: ShuffleLines(0)}
	Conformance(t, dec, c)
	rec := &recorder{}
	Conformance(rec, dec, c)
	require.Empty(t, rec.errs)

	wrap := wrapDecoder(dec, func(d stream.Decoder) stream.Decoder { return wrappingDecoder{d} })
	Conformance(t, wrap, Case{TRQ: testTRQ, Body: csvBody, Legacy: legacyFrom(wrap),
		Unwrap: func(ts timeseries.Timeseries) *dataset.DataSet { return ts.(wrapped).DataSet }})
}

func TestConformanceReportsDifferences(t *testing.T) {
	dec := wrapDecoder(csvDecoder(dataset.BuilderOptions{}),
		func(d stream.Decoder) stream.Decoder { return &countingDecoder{Decoder: d} })
	rec := &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody})
	rec.requireReported(t, "write-bytewise: differs from write-once: status")
}

func TestConformanceReportsFeedErrors(t *testing.T) {
	dec := wrapDecoder(csvDecoder(dataset.BuilderOptions{}),
		func(d stream.Decoder) stream.Decoder { return pickyDecoder{d} })
	rec := &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody})
	rec.requireReported(t, "write-bytewise: unexpected error: single byte write")

	failing := func(*timeseries.TimeRangeQuery) (stream.Decoder, error) { return nil, errPicky }
	rec = &recorder{}
	Conformance(rec, failing, Case{TRQ: testTRQ, Body: csvBody})
	require.Len(t, rec.errs, len(feeds)+2)
	Conformance(t, failing, Case{TRQ: testTRQ, Body: csvBody, WantErr: errPicky})
}

func TestConformanceWantErr(t *testing.T) {
	good := csvDecoder(dataset.BuilderOptions{})
	rec := &recorder{}
	Conformance(rec, good, Case{TRQ: testTRQ, Body: csvBody, WantErr: ErrAny, Legacy: legacyFrom(good)})
	rec.requireReported(t, "write-once: got error <nil>, want any error", "legacy: got no error")

	bad := []byte("1,a\n")
	Conformance(t, good, Case{TRQ: testTRQ, Body: bad, WantErr: ErrAny, Legacy: legacyFrom(good)})
	Conformance(t, good, Case{TRQ: testTRQ, Body: bad, WantErr: timeseries.ErrInvalidBody})
	rec = &recorder{}
	Conformance(rec, good, Case{TRQ: testTRQ, Body: bad, WantErr: timeseries.ErrInvalidTimeFormat})
	rec.requireReported(t, "want invalid time format")

	rec = &recorder{}
	Conformance(rec, good, Case{TRQ: testTRQ, Body: bad})
	rec.requireReported(t, "readfrom: unexpected error")
}

func TestConformanceWantAndLegacy(t *testing.T) {
	dec := csvDecoder(dataset.BuilderOptions{})
	other := csvDecoder(dataset.BuilderOptions{SeriesName: "other"})
	want, err := stream.BytesUnmarshaler(other)(csvBody, testTRQ)
	require.NoError(t, err)
	rec := &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody, Want: want.(*dataset.DataSet),
		Legacy: legacyFrom(other)})
	rec.requireReported(t, "differs from Want: results[0].series[0].name",
		"differs from legacy: results[0].series[0].name")

	failingLegacy := func(io.Reader, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return nil, errPicky
	}
	rec = &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody, Legacy: failingLegacy})
	rec.requireReported(t, "legacy: unexpected error: single byte write")
}

func TestConformanceShuffle(t *testing.T) {
	// with first-wins duplicates, the surviving value depends on row order
	dec := csvDecoder(dataset.BuilderOptions{Duplicates: dataset.DuplicatesFirstWins})
	body := []byte("1,a,x\n1,a,y\n1,a,z\n1,a,w\n2,a,v\n")
	rec := &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: body, Shuffle: ShuffleLines(0)})
	rec.requireReported(t, "differs from write-once: results[0].series[0].points[0].values[0]")

	failing := func(body []byte, _ *weaktest.Rand) []byte { return append(body, "x\n"...) }
	rec = &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody, Shuffle: failing})
	rec.requireReported(t, "shuffle-0: unexpected error")
}

func TestConformanceReadError(t *testing.T) {
	dec := wrapDecoder(csvDecoder(dataset.BuilderOptions{}),
		func(d stream.Decoder) stream.Decoder { return lenientDecoder{d} })
	rec := &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody})
	rec.requireReported(t, "read-error: got no error for a failed read")
}

func TestConformanceUnwrap(t *testing.T) {
	dec := wrapDecoder(csvDecoder(dataset.BuilderOptions{}),
		func(d stream.Decoder) stream.Decoder { return wrappingDecoder{d} })
	rec := &recorder{}
	Conformance(rec, dec, Case{TRQ: testTRQ, Body: csvBody})
	rec.requireReported(t, "write-once: got streamtest.wrapped, want a DataSet")
}

func TestShuffleLines(t *testing.T) {
	rng := weaktest.NewRand(1, 2)
	body := []byte("h1\nh2\na\nb\nc\nd\ne\nf")
	for range 8 {
		out := string(ShuffleLines(2)(body, rng))
		require.True(t, strings.HasPrefix(out, "h1\nh2\n"))
		require.True(t, strings.HasSuffix(out, "\n"))
		require.ElementsMatch(t, strings.Split("h1\nh2\na\nb\nc\nd\ne\nf", "\n"),
			strings.Split(strings.TrimSuffix(out, "\n"), "\n"))
	}
	require.Equal(t, "a\nb\n", string(ShuffleLines(5)([]byte("a\nb\n"), rng)))
	require.Empty(t, ShuffleLines(-1)(nil, rng))
	require.Equal(t, "a\n", string(ShuffleLines(-1)([]byte("a"), rng)))
}

func TestBench(t *testing.T) {
	bt := flag.Lookup("test.benchtime")
	require.NotNil(t, bt)
	prev := bt.Value.String()
	require.NoError(t, bt.Value.Set("3x"))
	t.Cleanup(func() { bt.Value.Set(prev) })
	u := stream.ReaderUnmarshaler(csvDecoder(dataset.BuilderOptions{}))
	res := testing.Benchmark(func(b *testing.B) { Bench(b, u, testTRQ, csvBody) })
	require.Positive(t, res.N)
	require.Equal(t, int64(len(csvBody)), res.Bytes)
}
