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

package remoteread

import (
	"errors"
	"math"
	"testing"

	"github.com/klauspost/compress/snappy"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

var errTest = errors.New("test kind")

var reader = Reader{Kind: errTest}

func appendBytes(dst []byte, num protowire.Number, b []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(dst, num, protowire.BytesType), b)
}

func appendVarint(dst []byte, num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(dst, num, protowire.VarintType), v)
}

func appendFixed64(dst []byte, num protowire.Number, v uint64) []byte {
	return protowire.AppendFixed64(protowire.AppendTag(dst, num, protowire.Fixed64Type), v)
}

func TestField(t *testing.T) {
	b := appendVarint(nil, 1, 7)
	b = appendFixed64(b, 2, 9)
	b = appendBytes(b, 3, []byte("x"))
	b = protowire.AppendFixed32(protowire.AppendTag(b, 4, protowire.Fixed32Type), 1)
	want := []Field{
		{Num: 1, Typ: protowire.VarintType, U: 7},
		{Num: 2, Typ: protowire.Fixed64Type, U: 9},
		{Num: 3, Typ: protowire.BytesType, B: []byte("x")},
		{Num: 4, Typ: protowire.Fixed32Type},
	}
	for _, w := range want {
		f, rest, err := reader.Field(b)
		require.NoError(t, err)
		require.Equal(t, w, f)
		b = rest
	}
	require.Empty(t, b)
	for name, bad := range map[string][]byte{
		"tag":    {0x80},
		"varint": {0x08, 0x80},
		"length": {0x0a, 0x05, 0x01},
	} {
		_, _, err := reader.Field(bad)
		require.ErrorIs(t, err, errTest, name)
	}
}

func TestExpectAndRepeated(t *testing.T) {
	require.NoError(t, reader.Expect(Field{Typ: protowire.VarintType}, protowire.VarintType))
	require.ErrorIs(t, reader.Expect(Field{Typ: protowire.Fixed64Type}, protowire.VarintType), errTest)

	n, err := reader.Repeated(Field{Typ: protowire.VarintType}, protowire.VarintType)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	n, err = reader.Repeated(Field{Typ: protowire.BytesType, B: make([]byte, 24)}, protowire.Fixed64Type)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	n, err = reader.Repeated(Field{Typ: protowire.BytesType, B: []byte{1, 0x80, 1, 2}}, protowire.VarintType)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	_, err = reader.Repeated(Field{Typ: protowire.BytesType, B: make([]byte, 7)}, protowire.Fixed64Type)
	require.ErrorIs(t, err, errTest, "packed fixed64 values of a partial value")
	_, err = reader.Repeated(Field{Typ: protowire.BytesType, B: []byte{0x80}}, protowire.VarintType)
	require.ErrorIs(t, err, errTest, "a truncated packed varint")
	_, err = reader.Repeated(Field{Typ: protowire.Fixed64Type}, protowire.VarintType)
	require.ErrorIs(t, err, errTest, "neither the scalar's type nor packed")
}

func TestLabel(t *testing.T) {
	b := appendBytes(appendBytes(appendVarint(nil, 3, 1), LabelName, []byte("job")), LabelValue, []byte("api"))
	name, value, err := reader.Label(b)
	require.NoError(t, err)
	require.Equal(t, "job", string(name))
	require.Equal(t, "api", string(value))
	_, _, err = reader.Label(appendVarint(nil, LabelName, 1))
	require.ErrorIs(t, err, errTest)
	_, _, err = reader.Label([]byte{0x0a, 0x09})
	require.ErrorIs(t, err, errTest)
}

func TestSample(t *testing.T) {
	v := math.Float64bits(1.5)
	// Prometheus's order, read directly, and the reverse, read field by field, with an unknown field
	for name, b := range map[string][]byte{
		"value first": appendVarint(appendFixed64(nil, SampleValue, v), SampleTimestamp, 42),
		"time first":  appendFixed64(appendVarint(appendVarint(nil, SampleTimestamp, 42), 9, 1), SampleValue, v),
	} {
		ts, f, err := reader.Sample(b)
		require.NoError(t, err, name)
		require.Equal(t, int64(42), ts, name)
		require.Equal(t, 1.5, f, name)
	}
	for name, b := range map[string][]byte{
		"value as varint":   appendVarint(nil, SampleValue, 1),
		"time as fixed64":   appendFixed64(nil, SampleTimestamp, 1),
		"malformed":         {0x80},
		"trailing past end": append(appendVarint(appendFixed64(nil, SampleValue, v), SampleTimestamp, 42), 0x80),
	} {
		_, _, err := reader.Sample(b)
		require.ErrorIs(t, err, errTest, name)
	}
}

func TestSeries(t *testing.T) {
	qr := appendBytes(appendVarint(nil, 2, 1), QueryResultTimeseries, []byte("a"))
	qr = appendBytes(qr, QueryResultTimeseries, []byte("b"))
	b := appendBytes(appendVarint(nil, 2, 1), ReadResponseResults, qr)
	b = appendBytes(b, ReadResponseResults, appendBytes(nil, QueryResultTimeseries, []byte("c")))
	b = appendBytes(b, ReadResponseResults, nil)
	type seen struct {
		result int
		series string
	}
	var got []seen
	n, err := reader.Series(b, func(result int, series []byte) error {
		got = append(got, seen{result, string(series)})
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, []seen{{0, "a"}, {0, "b"}, {1, "c"}}, got)

	stop := errors.New("stop")
	_, err = reader.Series(b, func(int, []byte) error { return stop })
	require.ErrorIs(t, err, stop)
	for name, bad := range map[string][]byte{
		"malformed":               {0x80},
		"results as varint":       appendVarint(nil, ReadResponseResults, 1),
		"malformed result":        appendBytes(nil, ReadResponseResults, []byte{0x80}),
		"time series as a varint": appendBytes(nil, ReadResponseResults, appendVarint(nil, QueryResultTimeseries, 1)),
	} {
		_, err := reader.Series(bad, func(int, []byte) error { return nil })
		require.ErrorIs(t, err, errTest, name)
	}
}

func TestDecodeSnappy(t *testing.T) {
	block := snappy.Encode(nil, []byte("0123456789"))
	b, err := DecodeSnappy(block, 10, errTest)
	require.NoError(t, err)
	require.Equal(t, "0123456789", string(b))
	b, err = DecodeSnappy(block, 0, errTest)
	require.NoError(t, err, "no limit")
	require.Len(t, b, 10)
	_, err = DecodeSnappy(block, 9, errTest)
	require.ErrorIs(t, err, errTest)
	_, err = DecodeSnappy([]byte{0xff}, 10, errTest)
	require.Error(t, err)
	require.NotErrorIs(t, err, errTest, "a malformed block is not one too large")
}

func TestDecodeAllowance(t *testing.T) {
	require.Equal(t, int64(DecodeExpansion*1000), DecodeAllowance(1000))
	require.Equal(t, int64(math.MaxInt64), DecodeAllowance(math.MaxInt64/2))
}
