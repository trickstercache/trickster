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

package engines

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// errWriter is an io.Writer that always returns an error.
type errWriter struct {
	err error
}

func (e *errWriter) Write([]byte) (int, error) {
	return 0, e.err
}

func TestSfResponseCaptureWrite(t *testing.T) {
	var inner bytes.Buffer
	c := &sfResponseCapture{inner: &inner}

	data := []byte("hello world")
	n, err := c.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}
	// verify tee: both inner and buffer should have the data
	if inner.String() != "hello world" {
		t.Errorf("inner: expected %q, got %q", "hello world", inner.String())
	}
	if c.buf.String() != "hello world" {
		t.Errorf("buf: expected %q, got %q", "hello world", c.buf.String())
	}
}

func TestSfResponseCaptureWriteError(t *testing.T) {
	injected := errors.New("write failed")
	c := &sfResponseCapture{inner: &errWriter{err: injected}}

	data := []byte("some data")
	n, err := c.Write(data)
	if !errors.Is(err, injected) {
		t.Errorf("expected injected error, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes written on error, got %d", n)
	}
	// buffer should still capture the data even when inner fails
	if c.buf.String() != "some data" {
		t.Errorf("buf should capture data regardless of inner error: got %q", c.buf.String())
	}
}

func TestSfResponseCaptureHeaderWithResponseWriter(t *testing.T) {
	rw := httptest.NewRecorder()
	rw.Header().Set("X-Test", "value")
	c := &sfResponseCapture{inner: rw}

	h := c.Header()
	if h.Get("X-Test") != "value" {
		t.Errorf("expected Header() to delegate to inner ResponseWriter")
	}
	// modifications should be reflected
	h.Set("X-New", "new")
	if rw.Header().Get("X-New") != "new" {
		t.Errorf("expected Header() to return inner's header map (same reference)")
	}
}

func TestSfResponseCaptureHeaderWithPlainWriter(t *testing.T) {
	c := &sfResponseCapture{inner: &bytes.Buffer{}}

	h := c.Header()
	if h == nil {
		t.Fatal("expected non-nil header")
	}
	if len(h) != 0 {
		t.Errorf("expected empty header for plain writer, got %v", h)
	}
}

func TestSfResponseCaptureWriteHeaderWithResponseWriter(t *testing.T) {
	rw := httptest.NewRecorder()
	c := &sfResponseCapture{inner: rw}

	c.WriteHeader(http.StatusNotFound)
	if rw.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rw.Code)
	}
}

func TestSfResponseCaptureWriteHeaderWithPlainWriter(t *testing.T) {
	// should not panic when inner is not an http.ResponseWriter
	c := &sfResponseCapture{inner: &bytes.Buffer{}}
	c.WriteHeader(http.StatusInternalServerError) // no-op, no panic
}

func TestSfResponseCaptureMultipleWrites(t *testing.T) {
	var inner bytes.Buffer
	c := &sfResponseCapture{inner: &inner}

	writes := []string{"one", "two", "three"}
	for _, s := range writes {
		if _, err := c.Write([]byte(s)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	expected := "onetwothree"
	if inner.String() != expected {
		t.Errorf("inner: expected %q, got %q", expected, inner.String())
	}
	if c.buf.String() != expected {
		t.Errorf("buf: expected %q, got %q", expected, c.buf.String())
	}
}
