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

// TestWrite_ReturnValue verifies that Write returns the number of bytes
// written in the current call, not a cumulative total. Returning the
// cumulative count violates the io.Writer contract and causes io.Copy to
// abort with errInvalidWrite on the second chunk.
func TestWrite_ReturnValue(t *testing.T) {
	sw := NewCaptureResponseWriter()

	a := []byte("hello")
	n1, err1 := sw.Write(a)
	require.NoError(t, err1)
	require.Equal(t, len(a), n1, "first Write must return len(a)")

	b := []byte(" world")
	n2, err2 := sw.Write(b)
	require.NoError(t, err2)
	require.Equal(t, len(b), n2,
		"second Write must return len(b), not cumulative len(a)+len(b)")
}

// TestWrite_IoCopy_LargeBody verifies that a body larger than io.Copy's
// internal 32KB buffer is fully copied without error. Before the fix,
// CaptureResponseWriter.Write returned a cumulative byte count which
// triggered errInvalidWrite in io.Copy on the second chunk, silently
// truncating the response to ~32KB.
func TestWrite_IoCopy_LargeBody(t *testing.T) {
	const size = 100 * 1024 // 100KB — well over the 32KB io.Copy buffer
	src := make([]byte, size)
	for i := range src {
		src[i] = byte(i % 251) // non-zero pattern for content verification
	}

	sw := NewCaptureResponseWriter()
	// Wrap in a plain io.Reader to strip the WriterTo interface from
	// bytes.Reader; otherwise io.Copy calls WriteTo which does a single
	// Write call and never exercises the multi-chunk read/write loop
	// that triggers the bug.
	n, err := io.Copy(sw, struct{ io.Reader }{bytes.NewReader(src)})
	require.NoError(t, err, "io.Copy must not return errInvalidWrite")
	require.Equal(t, int64(size), n, "io.Copy must report all bytes copied")
	require.Equal(t, src, sw.Body(), "captured body must match source")
}

// TestWrite_IoCopy_SmallBody verifies the happy path for bodies that fit
// in a single io.Copy chunk (<32KB).
func TestWrite_IoCopy_SmallBody(t *testing.T) {
	src := []byte("small body under 32KB")
	sw := NewCaptureResponseWriter()
	n, err := io.Copy(sw, struct{ io.Reader }{bytes.NewReader(src)})
	require.NoError(t, err)
	require.Equal(t, int64(len(src)), n)
	require.Equal(t, src, sw.Body())
}

// TestHeaderStatusCodeBody exercises the basic accessors.
func TestHeaderStatusCodeBody(t *testing.T) {
	sw := NewCaptureResponseWriter()

	// Default status is 200.
	require.Equal(t, http.StatusOK, sw.StatusCode())

	// WriteHeader(0) normalizes to 200.
	sw.WriteHeader(0)
	require.Equal(t, http.StatusOK, sw.StatusCode())

	sw.WriteHeader(http.StatusNotFound)
	require.Equal(t, http.StatusNotFound, sw.StatusCode())

	// Header returns a writable map.
	sw.Header().Set("X-Test", "value")
	require.Equal(t, "value", sw.Header().Get("X-Test"))

	// Body accumulates writes.
	sw.Write([]byte("ab"))
	sw.Write([]byte("cd"))
	require.Equal(t, []byte("abcd"), sw.Body())
}
