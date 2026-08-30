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
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestIsStreamingResponse(t *testing.T) {
	tests := []struct {
		name     string
		resp     *http.Response
		expected bool
	}{
		{"nil response", nil, false},
		{
			"server-sent events",
			&http.Response{
				Header:        http.Header{headers.NameContentType: {ContentTypeEventStream}},
				ContentLength: 42,
			},
			true,
		},
		{
			"server-sent events with charset",
			&http.Response{
				Header:        http.Header{headers.NameContentType: {"text/event-stream; charset=utf-8"}},
				ContentLength: 42,
			},
			true,
		},
		{
			"unknown length",
			&http.Response{Header: http.Header{}, ContentLength: -1},
			true,
		},
		{
			"known length json",
			&http.Response{
				Header:        http.Header{headers.NameContentType: {"application/json"}},
				ContentLength: 42,
			},
			false,
		},
		{
			"unparsable content type with known length",
			&http.Response{
				Header:        http.Header{headers.NameContentType: {"////"}},
				ContentLength: 42,
			},
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStreamingResponse(tc.resp); got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

type countingFlusher struct {
	http.ResponseWriter
	flushes int
}

func (c *countingFlusher) Flush() { c.flushes++ }

func TestStreamWriterFlushesEachWrite(t *testing.T) {
	cf := &countingFlusher{ResponseWriter: httptest.NewRecorder()}
	resp := &http.Response{Header: http.Header{}, ContentLength: -1}
	w := streamWriter(cf, resp)
	if w == io.Writer(cf) {
		t.Fatal("expected streaming response to be wrapped")
	}
	for range 3 {
		if _, err := w.Write([]byte("data: x\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	if cf.flushes != 3 {
		t.Errorf("expected 3 flushes, got %d", cf.flushes)
	}
}

func TestStreamWriterPassthrough(t *testing.T) {
	rec := httptest.NewRecorder()
	knownLen := &http.Response{
		Header:        http.Header{headers.NameContentType: {"application/json"}},
		ContentLength: 42,
	}
	if w := streamWriter(rec, knownLen); w != io.Writer(rec) {
		t.Error("non-streaming response should not be wrapped")
	}
	// a plain io.Writer cannot flush, so it must be returned unchanged
	buf := &bytes.Buffer{}
	streaming := &http.Response{Header: http.Header{}, ContentLength: -1}
	if w := streamWriter(buf, streaming); w != io.Writer(buf) {
		t.Error("non-ResponseWriter should not be wrapped")
	}
	if w := streamWriter(nil, streaming); w != nil {
		t.Error("nil writer should be returned unchanged")
	}
}

func TestFlushWriterReturnsWriteError(t *testing.T) {
	expected := errors.New("write failed")
	var flushed bool
	fw := &flushWriter{
		w:     &errWriter{err: expected},
		flush: func() error { flushed = true; return nil },
	}
	if _, err := fw.Write([]byte("x")); !errors.Is(err, expected) {
		t.Errorf("expected %v, got %v", expected, err)
	}
	if flushed {
		t.Error("should not flush when no bytes were written")
	}
}

func TestAbortOnCopyError(t *testing.T) {
	underServer := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	underServer = underServer.WithContext(
		context.WithValue(underServer.Context(), http.ServerContextKey, &http.Server{}))
	bare := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	copyErr := errors.New("unexpected EOF")

	tests := []struct {
		name        string
		w           io.Writer
		r           *http.Request
		err         error
		expectPanic bool
	}{
		{"nil error", httptest.NewRecorder(), underServer, nil, false},
		{"nil request", httptest.NewRecorder(), nil, copyErr, false},
		{"not a ResponseWriter", &bytes.Buffer{}, underServer, copyErr, false},
		{"no server in context", httptest.NewRecorder(), bare, copyErr, false},
		{"aborts under server", httptest.NewRecorder(), underServer, copyErr, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if tc.expectPanic && r != http.ErrAbortHandler {
					t.Errorf("expected http.ErrAbortHandler, got %v", r)
				}
				if !tc.expectPanic && r != nil {
					t.Errorf("unexpected panic: %v", r)
				}
			}()
			abortOnCopyError(tc.w, tc.r, tc.err)
		})
	}
}
