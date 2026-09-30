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

package handler

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zstd"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

type hijackResponseWriter struct {
	http.ResponseWriter
	conn net.Conn
	rw   *bufio.ReadWriter
}

func (w *hijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.rw, nil
}

type unwrapResponseWriter struct {
	http.ResponseWriter
}

func (w *unwrapResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func TestNewEncoder(t *testing.T) {
	w := NewEncoder(nil, nil)
	if w.(*responseEncoder).EncodingProfile == nil {
		t.Error("expected non-nil")
	}
}

func TestResponseEncoderHijack(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})

	rw := bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn))
	underlying := &hijackResponseWriter{
		ResponseWriter: httptest.NewRecorder(),
		conn:           serverConn,
		rw:             rw,
	}
	wrapped := &unwrapResponseWriter{ResponseWriter: underlying}
	ew := NewEncoder(wrapped, nil).(*responseEncoder)

	conn, gotRW, err := ew.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	if conn != serverConn {
		t.Error("connection mismatch")
	}
	if gotRW != rw {
		t.Error("buffered read-writer mismatch")
	}
	if ew.Unwrap() != wrapped {
		t.Error("underlying response writer mismatch")
	}
}

func TestResponseEncoderHijackUnsupported(t *testing.T) {
	ew := NewEncoder(httptest.NewRecorder(), nil).(*responseEncoder)
	_, _, err := ew.Hijack()
	if !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("expected http.ErrNotSupported, got %v", err)
	}
}

func TestWrite(t *testing.T) {
	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.writeFunc = ew.writeDirect

	ew.WriteHeader(http.StatusOK)
	ew.prepared = false

	i, err := ew.Write([]byte("trickster"))
	if i != 9 {
		t.Errorf("expected %d got %d", 9, i)
	}
	if err != nil {
		t.Error(err)
	}

	ew.encoder = &responseEncoder{ResponseWriter: w}
	ew.Close()
}

func TestSelectWriter(t *testing.T) {
	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.decoderInit = gzip.NewDecoder
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}

	ew.writeFunc = nil
	ew.encoder = &responseEncoder{ResponseWriter: w}
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}

	ew.writeFunc = nil
	ew.decoderInit = nil
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}

	ew.writeFunc = nil
	ew.encoder = nil
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}
}

func TestWriteEncoded(t *testing.T) {
	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew2 := &responseEncoder{ResponseWriter: w}
	ew.encoder = ew2
	i, err := ew.writeEncoded([]byte("trickster"))
	if i != 9 {
		t.Errorf("expected %d got %d", 9, i)
	}
	if err != nil {
		t.Error(err)
	}
}

// writes an encoded body through ew in parts of chunk bytes, and closes it
func writeInParts(t *testing.T, ew ResponseEncoder, body []byte, chunk int) {
	t.Helper()
	for b := body; len(b) > 0; {
		n := min(chunk, len(b))
		i, err := ew.Write(b[:n])
		if err != nil || i != n {
			t.Fatalf("write of %d bytes: %d, %v", n, i, err)
		}
		b = b[n:]
	}
	if err := ew.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteDecoded(t *testing.T) {
	want := benchJSONBody(200 << 10)
	encoded, err := gzip.Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	// a body that arrives in one write, and one that arrives in many
	for _, chunk := range []int{len(encoded), 1000} {
		w := httptest.NewRecorder()
		ew := &responseEncoder{ResponseWriter: w, decoderInit: gzip.NewDecoder}
		ew.selectWriter()
		ew.prepared = true
		writeInParts(t, ew, encoded, chunk)
		if !bytes.Equal(w.Body.Bytes(), want) {
			t.Errorf("chunk %d: got %d bytes, want %d", chunk, w.Body.Len(), len(want))
		}
		if ew.held != nil {
			t.Error("the held body was not released")
		}
	}
}

func TestWriteTranscoded(t *testing.T) {
	want := benchJSONBody(200 << 10)
	encoded, err := gzip.Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{len(encoded), 1000} {
		w := httptest.NewRecorder()
		ew := &responseEncoder{ResponseWriter: w, decoderInit: gzip.NewDecoder,
			encoder: zstd.NewEncoder(w, -1)}
		ew.selectWriter()
		ew.prepared = true
		writeInParts(t, ew, encoded, chunk)
		got, err := zstd.Decode(w.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("chunk %d: got %d bytes, want %d", chunk, len(got), len(want))
		}
	}
}

func TestWriteDecodedCorrupt(t *testing.T) {
	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w, decoderInit: gzip.NewDecoder}
	ew.selectWriter()
	ew.prepared = true
	if _, err := ew.Write([]byte("this is not a gzip stream")); err != nil {
		t.Fatal(err)
	}
	if err := ew.Close(); err == nil {
		t.Error("expected the decode to fail at close")
	}
}

func TestHandleCompressionDecodesStreamedBody(t *testing.T) {
	// through the middleware, as an engine serves a cached gzip object to a client that doesn't
	// accept gzip, in the parts a streamed cache body is copied in
	want := benchJSONBody(200 << 10)
	encoded, err := gzip.Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile.FromContext(r.Context()).ContentEncoding = providers.GZipValue
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		w.Header().Set(headers.NameContentEncoding, providers.GZipValue)
		for b := encoded; len(b) > 0; {
			n := min(1000, len(b))
			w.Write(b[:n])
			b = b[n:]
		}
	})
	h := HandleCompression(next, sets.New([]string{headers.ValueApplicationJSON}))
	r := httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if ce := w.Header().Get(headers.NameContentEncoding); ce != "" {
		t.Errorf("unexpected Content-Encoding %q", ce)
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Errorf("got %d bytes, want %d", w.Body.Len(), len(want))
	}
}

func TestPutHeldBufferDropsLargeBuffers(t *testing.T) {
	buf := bytes.NewBuffer(make([]byte, 0, maxPooledHeldBuffer+1))
	putHeldBuffer(buf)
	if got := heldBuffers.Get().(*bytes.Buffer); got == buf {
		t.Error("a buffer over the limit was pooled")
	}
}

func TestPrepareWriter(t *testing.T) {
	w := httptest.NewRecorder()
	h := w.Header()
	ep := &profile.Profile{
		Supported: 1, ContentType: headers.ValueTextPlain,
		CompressTypes: sets.New([]string{headers.ValueTextPlain}),
	}
	ew := &responseEncoder{EncodingProfile: ep, ResponseWriter: w}
	h.Set(headers.NameContentType, headers.ValueTextPlain)
	ew.prepareWriter()
	if ew.encoder == nil {
		t.Error("expected non-nil encoder")
	}

	ep.ContentEncoding = "gzip"
	ep.ContentEncodingNum = 2
	ew.prepareWriter()
	if ew.encoder == nil {
		t.Error("expected non-nil encoder")
	}
}

func TestResponseEncoderHijackedGuard(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})
	underlying := &hijackResponseWriter{
		ResponseWriter: httptest.NewRecorder(),
		conn:           serverConn,
		rw:             bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn)),
	}
	ew := NewEncoder(underlying, nil).(*responseEncoder)

	if _, _, err := ew.Hijack(); err != nil {
		t.Fatal(err)
	}
	if _, err := ew.Write([]byte("late")); !errors.Is(err, http.ErrHijacked) {
		t.Errorf("expected http.ErrHijacked, got %v", err)
	}
	if err := ew.Close(); err != nil {
		t.Errorf("close after hijack should be a no-op, got %v", err)
	}
}

func TestResponseEncoderNotHijackedOnError(t *testing.T) {
	ew := NewEncoder(httptest.NewRecorder(), nil).(*responseEncoder)
	if _, _, err := ew.Hijack(); err == nil {
		t.Fatal("expected hijack to fail on a non-hijackable writer")
	}
	if ew.hijacked {
		t.Error("failed hijack must not mark the writer as hijacked")
	}
	if _, err := ew.Write([]byte("ok")); err != nil {
		t.Errorf("writes must still work after a failed hijack, got %v", err)
	}
}

func TestResponseEncoderFlushError(t *testing.T) {
	// a gzip encoder buffers, so without a transform-aware flush the recorder
	// would still be empty after the controller flush
	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.encoder = gzip.NewEncoder(w, -1)
	ew.selectWriter()
	if _, err := ew.Write([]byte("trickster")); err != nil {
		t.Fatal(err)
	}
	// only the gzip header has reached the recorder; the payload is buffered
	buffered := w.Body.Len()
	if err := http.NewResponseController(ew).Flush(); err != nil {
		t.Fatal(err)
	}
	if w.Body.Len() <= buffered {
		t.Errorf("flush did not reach the encoder: %d bytes before and after", buffered)
	}
}

func TestResponseEncoderFlushNoEncoder(t *testing.T) {
	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	if err := ew.FlushError(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	ew.hijacked = true
	if err := ew.FlushError(); !errors.Is(err, http.ErrHijacked) {
		t.Errorf("expected ErrHijacked, got %v", err)
	}
}
