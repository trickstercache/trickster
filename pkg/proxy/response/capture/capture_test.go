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

package capture

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrite_ReturnValue(t *testing.T) {
	sw := NewCaptureResponseWriter()

	a := []byte("hello")
	n1, err1 := sw.Write(a)
	require.NoError(t, err1)
	require.Equal(t, len(a), n1)

	b := []byte(" world")
	n2, err2 := sw.Write(b)
	require.NoError(t, err2)
	require.Equal(t, len(b), n2)
}

func TestWrite_IoCopy_LargeBody(t *testing.T) {
	const size = 100 * 1024
	src := make([]byte, size)
	for i := range src {
		src[i] = byte(i % 251)
	}

	sw := NewCaptureResponseWriter()
	// Strip WriterTo so io.Copy takes the multi-chunk Read/Write loop.
	n, err := io.Copy(sw, struct{ io.Reader }{bytes.NewReader(src)})
	require.NoError(t, err)
	require.Equal(t, int64(size), n)
	require.Equal(t, src, sw.Body())
}

func TestWrite_IoCopy_SmallBody(t *testing.T) {
	src := []byte("small body under 32KB")
	sw := NewCaptureResponseWriter()
	n, err := io.Copy(sw, struct{ io.Reader }{bytes.NewReader(src)})
	require.NoError(t, err)
	require.Equal(t, int64(len(src)), n)
	require.Equal(t, src, sw.Body())
}

func TestWriteWithLimit_TruncatesAndFlags(t *testing.T) {
	sw := NewCaptureResponseWriterWithLimit(10)
	n, err := sw.Write([]byte("0123456789ABCDEF"))
	require.NoError(t, err)
	require.Equal(t, 16, n, "Write must report full input length even when truncating")
	require.Equal(t, []byte("0123456789"), sw.Body())
	require.True(t, sw.Truncated())
}

func TestWriteWithLimit_NoTruncationWhenUnderCap(t *testing.T) {
	sw := NewCaptureResponseWriterWithLimit(100)
	sw.Write([]byte("hello"))
	sw.Write([]byte(" world"))
	require.Equal(t, []byte("hello world"), sw.Body())
	require.False(t, sw.Truncated())
}

func TestWriteWithLimit_BoundaryExact(t *testing.T) {
	sw := NewCaptureResponseWriterWithLimit(11)
	sw.Write([]byte("hello world"))
	require.False(t, sw.Truncated())
	sw.Write([]byte("!"))
	require.True(t, sw.Truncated())
}

func TestWriteWithLimit_ZeroMeansUnlimited(t *testing.T) {
	sw := NewCaptureResponseWriterWithLimit(0)
	sw.Write(make([]byte, 1024*1024))
	require.False(t, sw.Truncated())
	require.Equal(t, 1024*1024, len(sw.Body()))
}

func TestWriteHeader_PresizeFromContentLength(t *testing.T) {
	sw := NewCaptureResponseWriter()
	sw.Header().Set("Content-Length", "8192")
	sw.WriteHeader(http.StatusOK)
	require.GreaterOrEqual(t, sw.body.Cap(), 8192)

	const size = 8192
	src := make([]byte, size)
	for i := range src {
		src[i] = byte(i)
	}
	n, err := sw.Write(src)
	require.NoError(t, err)
	require.Equal(t, size, n)
	require.Equal(t, src, sw.Body())
}

func TestWriteHeader_PresizeClampedByMaxBytes(t *testing.T) {
	const limit = 1024
	sw := NewCaptureResponseWriterWithLimit(limit)
	sw.Header().Set("Content-Length", strconv.Itoa(limit*16))
	sw.WriteHeader(http.StatusOK)
	require.LessOrEqual(t, sw.body.Cap(), limit, "presize must not exceed maxBytes")
}

func TestWriteHeader_PresizeSkippedWhenAbsentOrInvalid(t *testing.T) {
	cases := map[string]string{
		"missing":   "",
		"zero":      "0",
		"negative":  "-1",
		"malformed": "not-a-number",
	}
	for name, cl := range cases {
		t.Run(name, func(t *testing.T) {
			sw := NewCaptureResponseWriter()
			if cl != "" {
				sw.Header().Set("Content-Length", cl)
			}
			sw.WriteHeader(http.StatusOK)
			require.Equal(t, 0, sw.body.Cap(), "no presize expected")
			// Round-trip still works.
			sw.Write([]byte("ok"))
			require.Equal(t, []byte("ok"), sw.Body())
		})
	}
}

func TestWriteHeader_DoubleCallDoesNotRegrow(t *testing.T) {
	sw := NewCaptureResponseWriter()
	sw.Header().Set("Content-Length", "1024")
	sw.WriteHeader(http.StatusOK)
	firstCap := sw.body.Cap()
	require.GreaterOrEqual(t, firstCap, 1024)
	// Mutate the header to a much larger CL and call WriteHeader again; the
	// guard on Cap()!=0 must prevent re-grow.
	sw.Header().Set("Content-Length", "1048576")
	sw.WriteHeader(http.StatusOK)
	require.Equal(t, firstCap, sw.body.Cap(), "second WriteHeader must not re-grow")
}

// Fragmented writes mirror how upstream responses actually arrive (Transport
// reads in 4 KiB chunks). A single big Write would mask the doubling cost we
// care about, because bytes.Buffer.grow upsizes once to fit the entire input.
func benchmarkCaptureFragmented(b *testing.B, size, chunk int, withCL bool) {
	src := make([]byte, chunk)
	cl := strconv.Itoa(size)
	b.SetBytes(int64(size))
	for b.Loop() {
		sw := NewCaptureResponseWriter()
		if withCL {
			sw.Header().Set("Content-Length", cl)
		}
		sw.WriteHeader(http.StatusOK)
		for written := 0; written < size; written += chunk {
			sw.Write(src)
		}
	}
}

func BenchmarkCaptureWrite_10MB_4KChunks_WithCL(b *testing.B) {
	benchmarkCaptureFragmented(b, 10*1024*1024, 4*1024, true)
}

func BenchmarkCaptureWrite_10MB_4KChunks_NoCL(b *testing.B) {
	benchmarkCaptureFragmented(b, 10*1024*1024, 4*1024, false)
}

func BenchmarkCaptureWrite_100MB_4KChunks_WithCL(b *testing.B) {
	benchmarkCaptureFragmented(b, 100*1024*1024, 4*1024, true)
}

func BenchmarkCaptureWrite_100MB_4KChunks_NoCL(b *testing.B) {
	benchmarkCaptureFragmented(b, 100*1024*1024, 4*1024, false)
}

func TestHeaderStatusCodeBody(t *testing.T) {
	sw := NewCaptureResponseWriter()

	require.Equal(t, http.StatusOK, sw.StatusCode())

	sw.WriteHeader(0)
	require.Equal(t, http.StatusOK, sw.StatusCode())

	sw.WriteHeader(http.StatusNotFound)
	require.Equal(t, http.StatusNotFound, sw.StatusCode())

	sw.Header().Set("X-Test", "value")
	require.Equal(t, "value", sw.Header().Get("X-Test"))

	sw.Write([]byte("ab"))
	sw.Write([]byte("cd"))
	require.Equal(t, []byte("abcd"), sw.Body())
}
