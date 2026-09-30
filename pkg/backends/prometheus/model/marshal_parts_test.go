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
	"errors"
	"strconv"
	"testing"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/stretchr/testify/require"
)

var errWrite = errors.New("write failed")

type failingWriter struct {
	countingWriter
	failAfter int
}

func (w *failingWriter) Write(b []byte) (int, error) {
	if w.writes >= w.failAfter {
		w.writes++
		return 0, errWrite
	}
	return w.countingWriter.Write(b)
}

func TestMarshalWritesInParts(t *testing.T) {
	ds := benchMatrix(20, 500)
	var buf bytes.Buffer
	require.NoError(t, MarshalTSOrVectorWriter(ds, nil, 200, &buf, false))
	var w countingWriter
	require.NoError(t, MarshalTSOrVectorWriter(ds, nil, 200, &w, false))
	require.Equal(t, buf.Len(), w.bytes)
	require.Greater(t, w.writes, 1)
	require.LessOrEqual(t, w.writes, buf.Len()/tbytes.ChunkSize+1)
}

func TestMarshalStopsAtFailedWrite(t *testing.T) {
	ds := benchMatrix(20, 500)
	w := &failingWriter{failAfter: 1}
	require.ErrorIs(t, MarshalTSOrVectorWriter(ds, nil, 200, w, false), errWrite)
	// the failed write is the last one tried
	require.Equal(t, 2, w.writes)

	w = &failingWriter{}
	require.ErrorIs(t, marshalScalarWriter(benchMatrix(1, 1), 200, w), errWrite)
}

func TestAppendEpochSeconds(t *testing.T) {
	for _, e := range []epoch.Epoch{
		0, 1e9, -1e9, 1700000000e9, 1435781430123000000, 1435781430123456789,
		1435781430500000000, 1, -1, 9223372036000000000,
	} {
		want := strconv.FormatFloat(float64(e)/1e9, 'f', -1, 64)
		require.Equal(t, want, string(appendEpochSeconds(nil, e)), "epoch %d", e)
	}
}

func FuzzAppendEpochSeconds(f *testing.F) {
	f.Add(int64(1700000000))
	f.Add(int64(-5))
	f.Fuzz(func(t *testing.T, s int64) {
		// whole seconds are what the fast path writes; the rest go through FormatFloat as before
		if s > 9223372036 || s < -9223372036 {
			return
		}
		e := epoch.Epoch(s * 1e9)
		want := strconv.FormatFloat(float64(e)/1e9, 'f', -1, 64)
		if got := string(appendEpochSeconds(nil, e)); got != want {
			t.Fatalf("epoch %d: got %s want %s", e, got, want)
		}
	})
}

func TestMarshalLeavesUnsortedPointsInPlace(t *testing.T) {
	ds := benchMatrix(1, 3)
	pts := ds.Results[0].SeriesList[0].Points()
	pts[0], pts[2] = pts[2], pts[0]
	first := pts[0].Epoch
	var w countingWriter
	var buf bytes.Buffer
	require.NoError(t, MarshalTSOrVectorWriter(ds, nil, 200, &buf, false))
	require.NoError(t, MarshalTSOrVectorWriter(ds, nil, 200, &w, false))
	require.Equal(t, first, pts[0].Epoch, "the marshal sorted the dataset's own points")
	require.Contains(t, buf.String(), `"values":[[1700000000,`)
}
