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

package bytes

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type plainWriter struct {
	buf    bytes.Buffer
	chunks int
}

func (w *plainWriter) Write(b []byte) (int, error) {
	w.chunks++
	return w.buf.Write(b)
}

type readerFromWriter struct {
	plainWriter
	readFrom bool
}

func (w *readerFromWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom = true
	return w.buf.ReadFrom(r)
}

func TestCopy(t *testing.T) {
	body := strings.Repeat("trickster ", CopyBufferSize/5)

	// neither side can copy alone, so the pooled buffer carries it in CopyBufferSize chunks
	w := &plainWriter{}
	n, err := Copy(w, io.LimitReader(strings.NewReader(body), int64(len(body))))
	require.NoError(t, err)
	require.EqualValues(t, len(body), n)
	require.Equal(t, body, w.buf.String())
	require.Equal(t, 2, w.chunks)

	// a source that writes itself out is left to do so
	w = &plainWriter{}
	_, err = Copy(w, strings.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, body, w.buf.String())
	require.Equal(t, 1, w.chunks)

	// as is a destination that reads for itself
	rf := &readerFromWriter{}
	_, err = Copy(rf, io.LimitReader(strings.NewReader(body), int64(len(body))))
	require.NoError(t, err)
	require.True(t, rf.readFrom)
	require.Equal(t, body, rf.buf.String())
}

func TestPutCopyBufferDropsOtherSizes(t *testing.T) {
	small := make([]byte, 16)
	PutCopyBuffer(&small)
	for range 4 {
		bp := GetCopyBuffer()
		require.Len(t, *bp, CopyBufferSize)
		PutCopyBuffer(bp)
	}
	bp := GetCopyBuffer()
	*bp = (*bp)[:10]
	PutCopyBuffer(bp)
	require.Len(t, *GetCopyBuffer(), CopyBufferSize)
}

func BenchmarkCopy(b *testing.B) {
	body := []byte(strings.Repeat("trickster ", 100<<10))
	b.Run("io.Copy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			io.Copy(io.MultiWriter(io.Discard), io.LimitReader(bytes.NewReader(body), int64(len(body))))
		}
	})
	b.Run("Copy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Copy(io.MultiWriter(io.Discard), io.LimitReader(bytes.NewReader(body), int64(len(body))))
		}
	})
}

type errAfterReader struct {
	r   io.Reader
	err error
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		return n, e.err
	}
	return n, err
}

func TestReadAllSized(t *testing.T) {
	body := strings.Repeat("trickster ", 1000)
	for _, size := range []int64{0, -1, int64(len(body)), int64(len(body) - 100), int64(len(body) + 100)} {
		// a reader without WriterTo, read in small parts as a response body is
		got, err := ReadAllSized(io.LimitReader(strings.NewReader(body), int64(len(body))), size)
		require.NoError(t, err, size)
		require.Equal(t, body, string(got), size)
	}
	got, err := ReadAllSized(strings.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	require.Equal(t, len(body)+1, cap(got), "a right size is read into one allocation")
	_, err = ReadAllSized(&errAfterReader{r: strings.NewReader("abc"), err: errTestRead}, 3)
	require.ErrorIs(t, err, errTestRead)
}

var errTestRead = errors.New("read failed")
