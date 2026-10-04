/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	tgzip "github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// vmselect writes isPartial right after status when some vmstorage nodes didn't answer, so the
// first bytes of a response say whether it is complete; single-node VictoriaMetrics never writes it
const partialPeekBytes = 256

var (
	isPartialKey = []byte(`"isPartial"`)
	// ErrPartialResponse is returned when decoding a response VictoriaMetrics marked partial, so
	// the delta proxy cache relays the request instead of caching an incomplete result.
	ErrPartialResponse = errors.New("victoriametrics returned a partial response")
)

// isPartial reports whether a JSON response's leading bytes mark it partial.
func isPartial(head []byte) bool {
	_, after, ok := bytes.Cut(head, isPartialKey)
	if !ok {
		return false
	}
	rest := bytes.TrimLeft(after, " \t\r\n")
	if len(rest) == 0 || rest[0] != ':' {
		return false
	}
	return bytes.HasPrefix(bytes.TrimLeft(rest[1:], " \t\r\n"), []byte("true"))
}

// detectPartialResponses keeps partial responses out of both caches: the modeler refuses them, so
// a range query falls back to relaying, and the transport marks other API responses no-store. An
// origin that reports isPartial (vmselect) has it written into the complete responses served.
func (c *Client) detectPartialResponses(m *timeseries.Modeler, hc *http.Client) {
	if m != nil && m.WireUnmarshalerReader != nil {
		next := m.WireUnmarshalerReader
		m.WireUnmarshalerReader = func(r io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
			br := bufio.NewReaderSize(r, partialPeekBytes)
			head, _ := br.Peek(partialPeekBytes)
			if err := c.checkPartial(head); err != nil {
				return nil, err
			}
			return next(br, trq)
		}
	}
	if m != nil && m.WireUnmarshaler != nil {
		next := m.WireUnmarshaler
		m.WireUnmarshaler = func(b []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
			if err := c.checkPartial(b[:min(len(b), partialPeekBytes)]); err != nil {
				return nil, err
			}
			return next(b, trq)
		}
	}
	if m != nil && m.WireMarshalWriter != nil {
		next := m.WireMarshalWriter
		m.WireMarshalWriter = func(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
			if !c.reportsPartial.Load() {
				return next(ts, rlo, status, w)
			}
			pw := newIsPartialWriter(w)
			err := next(ts, rlo, status, pw)
			if ferr := pw.flush(); err == nil {
				err = ferr
			}
			return err
		}
	}
	if m != nil && m.WireMarshaler != nil {
		next := m.WireMarshaler
		m.WireMarshaler = func(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
			b, err := next(ts, rlo, status)
			if err != nil || !c.reportsPartial.Load() || !bytes.HasPrefix(b, successPrefix) {
				return b, err
			}
			out := make([]byte, 0, len(b)+len(isPartialFalse))
			out = append(append(append(out, successPrefix...), isPartialFalse...), b[len(successPrefix):]...)
			return out, nil
		}
	}
	if hc != nil && hc.Transport != nil {
		hc.Transport = &partialTransport{next: hc.Transport}
	}
}

// checkPartial refuses a partial response and records whether the origin reports isPartial.
func (c *Client) checkPartial(head []byte) error {
	if !bytes.Contains(head, isPartialKey) {
		return nil
	}
	if isPartial(head) {
		return ErrPartialResponse
	}
	c.reportsPartial.Store(true)
	return nil
}

var (
	successPrefix  = []byte(`{"status":"success"`)
	isPartialFalse = []byte(`,"isPartial":false`)
)

// isPartialWriter writes isPartial after a success envelope's status, where vmselect writes it.
// It keeps the http.ResponseWriter it wraps, so the marshaler still sets the status and headers.
type isPartialWriter struct {
	io.Writer
	rw   http.ResponseWriter
	head []byte
	done bool
}

type isPartialResponseWriter struct {
	*isPartialWriter
}

func (w isPartialResponseWriter) Header() http.Header  { return w.rw.Header() }
func (w isPartialResponseWriter) WriteHeader(code int) { w.rw.WriteHeader(code) }

func newIsPartialWriter(w io.Writer) interface {
	io.Writer
	flush() error
} {
	pw := &isPartialWriter{Writer: w}
	if rw, ok := w.(http.ResponseWriter); ok {
		pw.rw = rw
		return isPartialResponseWriter{pw}
	}
	return pw
}

func (w *isPartialWriter) Write(b []byte) (int, error) {
	if w.done {
		return w.Writer.Write(b)
	}
	w.head = append(w.head, b...)
	if len(w.head) < len(successPrefix) {
		return len(b), nil
	}
	return len(b), w.flush()
}

// flush writes the held envelope opening, with isPartial when it opens a success response.
func (w *isPartialWriter) flush() error {
	if w.done || len(w.head) == 0 {
		return nil
	}
	w.done = true
	head := w.head
	if bytes.HasPrefix(head, successPrefix) {
		if _, err := w.Writer.Write(successPrefix); err != nil {
			return err
		}
		if _, err := w.Writer.Write(isPartialFalse); err != nil {
			return err
		}
		head = head[len(successPrefix):]
	}
	_, err := w.Writer.Write(head)
	return err
}

// partialTransport marks a partial MetricsQL API response no-store, which outweighs the shared
// max-age that the object proxy cache's path configuration appends.
type partialTransport struct {
	next http.RoundTripper
}

func (t *partialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK || resp.Body == nil ||
		!inspectsPartial(r.URL.Path) {
		return resp, err
	}
	head, body, ok := peekDecoded(resp.Body, resp.Header.Get(headers.NameContentEncoding))
	resp.Body = body
	if !ok || isPartial(head) {
		resp.Header.Set(headers.NameCacheControl, headers.ValueNoStore)
	}
	return resp, nil
}

// inspectsPartial reports whether path is a MetricsQL API read the object proxy cache stores;
// range queries are checked as they are decoded instead.
func inspectsPartial(path string) bool {
	return strings.Contains(path, prometheus.APIPath) && !strings.HasSuffix(path, "query_range")
}

// peekDecoded returns a response body's first decoded bytes and a body that still yields every
// original byte. Encodings other than gzip, which is all VictoriaMetrics sends, aren't peeked.
func peekDecoded(rc io.ReadCloser, encoding string) ([]byte, io.ReadCloser, bool) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		br := bufio.NewReaderSize(rc, partialPeekBytes)
		head, _ := br.Peek(partialPeekBytes)
		return head, readCloser{br, rc}, true
	case "gzip", "x-gzip":
		// the compressed bytes the decoder reads are kept, so the body can be passed on unchanged
		var consumed bytes.Buffer
		zr := tgzip.NewDecoder(io.TeeReader(rc, &consumed))
		head := make([]byte, partialPeekBytes)
		n, err := io.ReadFull(zr, head)
		_ = zr.Close()
		body := readCloser{io.MultiReader(bytes.NewReader(consumed.Bytes()), rc), rc}
		// a body shorter than the peek ends early; a header that isn't gzip yields nothing
		ok := err == nil || (n > 0 && errors.Is(err, io.ErrUnexpectedEOF)) || (n == 0 && errors.Is(err, io.EOF))
		return head[:n], body, ok
	}
	return nil, rc, false
}

type readCloser struct {
	io.Reader
	closer io.Closer
}

func (r readCloser) Close() error { return r.closer.Close() }
