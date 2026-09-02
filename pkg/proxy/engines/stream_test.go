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
	"strings"
	"testing"
	"time"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
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
	// HTTP/3 carries quic-go's own server key, so the served marker stands in
	h3 := httptest.NewRequest(http.MethodGet, "http://trickstercache.org/", nil)
	h3 = h3.WithContext(tctx.WithServed(h3.Context()))
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
		{"aborts for http/3", httptest.NewRecorder(), h3, copyErr, true},
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

type stallingReader struct {
	data    []byte
	stall   chan struct{}
	stalled bool
	closed  chan struct{}
}

func (s *stallingReader) Read(p []byte) (int, error) {
	if !s.stalled && len(s.data) > 0 {
		n := copy(p, s.data)
		s.data = s.data[n:]
		if len(s.data) == 0 {
			s.stalled = true
		}
		return n, nil
	}
	// block until the body is closed, which is how a stalled origin presents
	<-s.closed
	return 0, io.EOF
}

func (s *stallingReader) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func TestIdleTimeoutBodyStall(t *testing.T) {
	sr := &stallingReader{data: []byte("partial"), closed: make(chan struct{})}
	b := newIdleTimeoutBody(sr, 50*time.Millisecond)

	buf := make([]byte, 16)
	n, err := b.Read(buf)
	if err != nil || n != 7 {
		t.Fatalf("first read should succeed: n=%d err=%v", n, err)
	}
	// the origin now stalls; the idle deadline must abort the read
	_, err = b.Read(buf)
	if !errors.Is(err, tpe.ErrOriginStalled) {
		t.Errorf("expected ErrOriginStalled, got %v", err)
	}
}

func TestIdleTimeoutBodyProgressResets(t *testing.T) {
	// a slow but progressing body must not be cut off, however long it runs
	src := io.NopCloser(&slowReader{chunks: 6, delay: 20 * time.Millisecond})
	b := newIdleTimeoutBody(src, 100*time.Millisecond)
	defer b.Close()
	n, err := io.Copy(io.Discard, b)
	if err != nil {
		t.Fatalf("progressing body should not time out: %v", err)
	}
	if n != 6 {
		t.Errorf("expected 6 bytes, got %d", n)
	}
}

type slowReader struct {
	chunks int
	delay  time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	s.chunks--
	p[0] = 'x'
	return 1, nil
}

func TestNewIdleTimeoutBodyPassthrough(t *testing.T) {
	if newIdleTimeoutBody(nil, time.Second) != nil {
		t.Error("nil body should stay nil")
	}
	rc := io.NopCloser(strings.NewReader("x"))
	if got := newIdleTimeoutBody(rc, 0); got != rc {
		t.Error("a non-positive timeout should return the body unwrapped")
	}
}
