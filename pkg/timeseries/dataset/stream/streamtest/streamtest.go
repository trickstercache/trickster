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

// Package streamtest checks that stream.Decoder implementations produce the same
// result however their input arrives, and benchmarks them against other unmarshalers.
package streamtest

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strconv"
	"testing"
	"testing/iotest"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// ErrAny, as a Case's WantErr, accepts any non-nil error.
var ErrAny = errors.New("any error")

var errInjected = errors.New("injected read error")

const shuffleRounds = 4

// Case describes one upstream response body for Conformance.
type Case struct {
	// TRQ is passed to every decoder and unmarshaler.
	TRQ *timeseries.TimeRangeQuery
	// Body is the upstream response body.
	Body []byte
	// WantErr, when set, is the error every decode must return, per errors.Is.
	WantErr error
	// Want, when set, is the DataSet every decode must produce, ignoring sizes.
	Want *dataset.DataSet
	// Legacy, when set, is an existing unmarshaler whose DataSet must match, ignoring sizes.
	Legacy timeseries.UnmarshalerReaderFunc
	// Shuffle, when set, reorders Body without changing its meaning; see ShuffleLines.
	Shuffle func(body []byte, rng *weaktest.Rand) []byte
	// Unwrap returns the DataSet within a decoded Timeseries. When nil, the
	// Timeseries must be a *dataset.DataSet.
	Unwrap func(timeseries.Timeseries) *dataset.DataSet
}

type outcome struct {
	name string
	ts   timeseries.Timeseries
	err  error
}

type feed struct {
	name string
	run  func(dec stream.Decoder, body []byte) error
}

type readerOnly struct{ io.Reader }

var feeds = []feed{
	{"write-once", func(dec stream.Decoder, body []byte) error {
		_, err := dec.Write(body)
		return err
	}},
	{"write-bytewise", func(dec stream.Decoder, body []byte) error {
		for i := range body {
			if _, err := dec.Write(body[i : i+1]); err != nil {
				return err
			}
		}
		return nil
	}},
	{"write-random", func(dec stream.Decoder, body []byte) error {
		rng := weaktest.NewRand(0x5eed, 0x5eed)
		for rest := body; len(rest) > 0; {
			n := min(1+rng.IntN(64), len(rest))
			if _, err := dec.Write(rest[:n]); err != nil {
				return err
			}
			rest = rest[n:]
		}
		return nil
	}},
	{"write-then-readfrom", func(dec stream.Decoder, body []byte) error {
		half := len(body) / 2
		if _, err := dec.Write(body[:half]); err != nil {
			return err
		}
		_, err := dec.ReadFrom(readerOnly{bytes.NewReader(body[half:])})
		return err
	}},
	{"readfrom", func(dec stream.Decoder, body []byte) error {
		_, err := dec.ReadFrom(bytes.NewReader(body))
		return err
	}},
	{"readfrom-reader", func(dec stream.Decoder, body []byte) error {
		_, err := dec.ReadFrom(readerOnly{bytes.NewReader(body)})
		return err
	}},
	{"readfrom-onebyte", func(dec stream.Decoder, body []byte) error {
		_, err := dec.ReadFrom(iotest.OneByteReader(bytes.NewReader(body)))
		return err
	}},
	{"readfrom-half", func(dec stream.Decoder, body []byte) error {
		_, err := dec.ReadFrom(iotest.HalfReader(bytes.NewReader(body)))
		return err
	}},
	{"readfrom-dataerr", func(dec stream.Decoder, body []byte) error {
		_, err := dec.ReadFrom(iotest.DataErrReader(bytes.NewReader(body)))
		return err
	}},
}

// Conformance decodes c.Body through every way a Decoder can be fed, including the
// stream adapters, and reports each result that differs or fails via t.Error or t.Errorf.
func Conformance(t testing.TB, newDecoder stream.NewDecoderFunc, c Case) {
	t.Helper()
	unwrap := c.Unwrap
	if unwrap == nil {
		unwrap = asDataSet
	}
	outcomes := make([]outcome, 0, len(feeds)+2)
	for _, f := range feeds {
		ts, err := decodeWith(newDecoder, c.TRQ, f.run, c.Body)
		outcomes = append(outcomes, outcome{f.name, ts, err})
	}
	ts, err := stream.ReaderUnmarshaler(newDecoder)(bytes.NewReader(c.Body), c.TRQ)
	outcomes = append(outcomes, outcome{"reader-unmarshaler", ts, err})
	ts, err = stream.BytesUnmarshaler(newDecoder)(c.Body, c.TRQ)
	outcomes = append(outcomes, outcome{"bytes-unmarshaler", ts, err})

	if c.WantErr != nil {
		for _, o := range outcomes {
			if !errorMatches(o.err, c.WantErr) {
				t.Errorf("%s: got error %v, want %v", o.name, o.err, c.WantErr)
			}
		}
		if c.Legacy != nil {
			if _, err := c.Legacy(bytes.NewReader(c.Body), c.TRQ); err == nil {
				t.Error("legacy: got no error, want one")
			}
		}
		return
	}

	var base *dataset.DataSet
	var baseName string
	for _, o := range outcomes {
		ds := checkOutcome(t, unwrap, o)
		if ds == nil {
			continue
		}
		if base == nil {
			base, baseName = ds, o.name
			continue
		}
		if err := Compare(base, ds, CompareOptions{}); err != nil {
			t.Errorf("%s: differs from %s: %v", o.name, baseName, err)
		}
	}
	if base == nil {
		return
	}
	if c.Want != nil {
		if err := Compare(c.Want, base, CompareOptions{IgnoreSizes: true}); err != nil {
			t.Errorf("%s: differs from Want: %v", baseName, err)
		}
	}
	if c.Legacy != nil {
		ts, err := c.Legacy(bytes.NewReader(c.Body), c.TRQ)
		if ds := checkOutcome(t, unwrap, outcome{"legacy", ts, err}); ds != nil {
			if err := Compare(ds, base, CompareOptions{IgnoreSizes: true}); err != nil {
				t.Errorf("%s: differs from legacy: %v", baseName, err)
			}
		}
	}
	if c.Shuffle != nil {
		for i := range uint64(shuffleRounds) {
			rng := weaktest.NewRand(i, 0x5eed)
			ts, err := stream.BytesUnmarshaler(newDecoder)(c.Shuffle(slices.Clone(c.Body), rng), c.TRQ)
			o := outcome{"shuffle-" + strconv.FormatUint(i, 10), ts, err}
			if ds := checkOutcome(t, unwrap, o); ds != nil {
				if err := Compare(base, ds, CompareOptions{IgnoreSeriesOrder: true}); err != nil {
					t.Errorf("%s: differs from %s: %v", o.name, baseName, err)
				}
			}
		}
	}
	// a failed read must surface as an error rather than as a partial result
	r := io.MultiReader(bytes.NewReader(c.Body[:len(c.Body)/2]), iotest.ErrReader(errInjected))
	if _, err := stream.ReaderUnmarshaler(newDecoder)(r, c.TRQ); err == nil {
		t.Error("read-error: got no error for a failed read")
	}
}

// ShuffleLines returns a Case.Shuffle that keeps the first header lines in place
// and shuffles the rest, for formats whose rows may arrive in any order.
func ShuffleLines(header int) func(body []byte, rng *weaktest.Rand) []byte {
	return func(body []byte, rng *weaktest.Rand) []byte {
		lines := bytes.SplitAfter(body, []byte{'\n'})
		if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
			lines = lines[:n-1]
		}
		if n := len(lines); n > 0 && !bytes.HasSuffix(lines[n-1], []byte{'\n'}) {
			lines[n-1] = append(slices.Clip(lines[n-1]), '\n')
		}
		if header < len(lines) {
			rows := lines[max(header, 0):]
			rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		}
		return bytes.Join(lines, nil)
	}
}

// Bench measures u decoding body, as the proxy engine calls it, so a stream
// decoder and an existing unmarshaler can be compared.
func Bench(b *testing.B, u timeseries.UnmarshalerReaderFunc,
	trq *timeseries.TimeRangeQuery, body []byte,
) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	r := bytes.NewReader(body)
	for b.Loop() {
		r.Reset(body)
		if _, err := u(io.NopCloser(r), trq); err != nil {
			b.Fatal(err)
		}
	}
}

func decodeWith(newDecoder stream.NewDecoderFunc, trq *timeseries.TimeRangeQuery,
	run func(stream.Decoder, []byte) error, body []byte,
) (timeseries.Timeseries, error) {
	dec, err := newDecoder(trq)
	if err != nil {
		return nil, err
	}
	if err := run(dec, body); err != nil {
		return nil, err
	}
	return dec.Finish()
}

func checkOutcome(t testing.TB, unwrap func(timeseries.Timeseries) *dataset.DataSet,
	o outcome,
) *dataset.DataSet {
	t.Helper()
	if o.err != nil {
		t.Errorf("%s: unexpected error: %v", o.name, o.err)
		return nil
	}
	ds := unwrap(o.ts)
	if ds == nil {
		t.Errorf("%s: got %T, want a DataSet", o.name, o.ts)
	}
	return ds
}

func asDataSet(ts timeseries.Timeseries) *dataset.DataSet {
	ds, _ := ts.(*dataset.DataSet)
	return ds
}

func errorMatches(err, want error) bool {
	if err == nil {
		return false
	}
	return errors.Is(want, ErrAny) || errors.Is(err, want)
}
