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
