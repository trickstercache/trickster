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
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestIsPartial(t *testing.T) {
	for in, want := range map[string]bool{
		`{"status":"success","isPartial":true,"data":{}}`:     true,
		`{"status":"success", "isPartial" : true ,"data":{}}`: true,
		`{"status":"success","isPartial":false,"data":{}}`:    false,
		`{"status":"success","data":{}}`:                      false,
		`{"status":"success","isPartial"`:                     false,
		`{"status":"success","isPartial" "true"`:              false,
	} {
		if isPartial([]byte(in)) != want {
			t.Errorf("%s: want %v", in, want)
		}
	}
}

type fakeModeler struct{ reads, unmarshals int }

func (f *fakeModeler) modeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireUnmarshalerReader: func(r io.Reader, _ *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
			f.reads++
			b, _ := io.ReadAll(r)
			if !bytes.HasPrefix(b, []byte("{")) {
				return nil, errors.New("not the whole body")
			}
			return nil, nil
		},
		WireUnmarshaler: func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
			f.unmarshals++
			return nil, nil
		},
		WireMarshalWriter: func(_ timeseries.Timeseries, _ *timeseries.RequestOptions, status int, w io.Writer) error {
			if rw, ok := w.(http.ResponseWriter); ok {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(status)
			}
			for _, part := range []string{`{"stat`, `us":"success"`, `,"data":{}}`} {
				if _, err := io.WriteString(w, part); err != nil {
					return err
				}
			}
			return nil
		},
		WireMarshaler: func(timeseries.Timeseries, *timeseries.RequestOptions, int) ([]byte, error) {
			return []byte(`{"status":"success","data":{}}`), nil
		},
	}
}

func TestModelerDetectsPartialResponses(t *testing.T) {
	c := &Client{}
	f := &fakeModeler{}
	m := f.modeler()
	c.detectPartialResponses(m, nil)
	partial := `{"status":"success","isPartial":true,"data":{}}`
	if _, err := m.WireUnmarshalerReader(strings.NewReader(partial), nil); !errors.Is(err, ErrPartialResponse) {
		t.Errorf("reader: got %v", err)
	}
	if _, err := m.WireUnmarshaler([]byte(partial), nil); !errors.Is(err, ErrPartialResponse) {
		t.Errorf("bytes: got %v", err)
	}
	if f.reads != 0 || f.unmarshals != 0 || c.reportsPartial.Load() {
		t.Fatal("a partial response was decoded")
	}

	// a single-node response is decoded whole, and served without isPartial
	if _, err := m.WireUnmarshalerReader(strings.NewReader(`{"status":"success","data":{}}`), nil); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := m.WireMarshalWriter(nil, nil, 200, &buf); err != nil || buf.String() != `{"status":"success","data":{}}` {
		t.Fatalf("got %s, %v", buf.String(), err)
	}

	// once the origin reports isPartial, served responses carry it as vmselect writes them
	if _, err := m.WireUnmarshaler([]byte(`{"status":"success","isPartial":false,"data":{}}`), nil); err != nil {
		t.Fatal(err)
	}
	if !c.reportsPartial.Load() {
		t.Fatal("the origin's isPartial was not recorded")
	}
	want := `{"status":"success","isPartial":false,"data":{}}`
	buf.Reset()
	if err := m.WireMarshalWriter(nil, nil, 200, &buf); err != nil || buf.String() != want {
		t.Errorf("writer: got %s, %v", buf.String(), err)
	}
	rec := httptest.NewRecorder()
	if err := m.WireMarshalWriter(nil, nil, 206, rec); err != nil || rec.Body.String() != want ||
		rec.Code != 206 || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("response writer: %d %v %s %v", rec.Code, rec.Header(), rec.Body.String(), err)
	}
	if b, err := m.WireMarshaler(nil, nil, 200); err != nil || string(b) != want {
		t.Errorf("marshaler: got %s, %v", b, err)
	}
}

func TestIsPartialWriter(t *testing.T) {
	for in, want := range map[string]string{
		`{"status":"error","error":"x"}`:      `{"status":"error","error":"x"}`,
		`{"status":"success"}`:                `{"status":"success","isPartial":false}`,
		`{"s"`:                                `{"s"`,
		`{"status":"success","warnings":[1]}`: `{"status":"success","isPartial":false,"warnings":[1]}`,
	} {
		var buf bytes.Buffer
		pw := newIsPartialWriter(&buf)
		for i := range len(in) {
			if _, err := pw.Write([]byte{in[i]}); err != nil {
				t.Fatal(err)
			}
		}
		if err := pw.flush(); err != nil || buf.String() != want {
			t.Errorf("%s: got %s, %v", in, buf.String(), err)
		}
	}
	pw := newIsPartialWriter(failingWriter{})
	if _, err := pw.Write([]byte(`{"status":"success","data":{}}`)); err == nil {
		t.Error("a failed write was not reported")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type staticTransport struct {
	resp *http.Response
	err  error
}

func (s staticTransport) RoundTrip(*http.Request) (*http.Response, error) { return s.resp, s.err }

func gzipped(s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = io.WriteString(zw, s)
	_ = zw.Close()
	return b.Bytes()
}

func TestPartialTransport(t *testing.T) {
	partial := `{"status":"success","isPartial":true,"data":{"result":[` + strings.Repeat(`1,`, 4096) + `1]}}`
	complete := strings.Replace(partial, "true", "false", 1)
	tests := []struct {
		name, path, encoding string
		body                 []byte
		code                 int
		noStore              bool
	}{
		{"partial", "/select/0/prometheus/api/v1/query", "", []byte(partial), 200, true},
		{"complete", "/api/v1/labels", "", []byte(complete), 200, false},
		{"gzip partial", "/api/v1/series", "gzip", gzipped(partial), 200, true},
		{"gzip complete", "/api/v1/series", "gzip", gzipped(complete), 200, false},
		{"short gzip", "/api/v1/series", "gzip", gzipped(`{"status":"success","data":[]}`), 200, false},
		{"empty gzip", "/api/v1/series", "gzip", gzipped(""), 200, false},
		{"bad gzip", "/api/v1/series", "gzip", []byte("not gzip"), 200, true},
		{"other encoding", "/api/v1/series", "br", []byte(partial), 200, true},
		{"range query", "/api/v1/query_range", "", []byte(partial), 200, false},
		{"graphite", "/render", "", []byte(partial), 200, false},
		{"error status", "/api/v1/query", "", []byte(partial), 400, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: test.code, Header: http.Header{},
				Body: io.NopCloser(bytes.NewReader(test.body)),
			}
			if test.encoding != "" {
				resp.Header.Set(headers.NameContentEncoding, test.encoding)
			}
			tr := &partialTransport{next: staticTransport{resp: resp}}
			got, err := tr.RoundTrip(httptest.NewRequest(http.MethodGet, test.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			if noStore := got.Header.Get(headers.NameCacheControl) == headers.ValueNoStore; noStore != test.noStore {
				t.Errorf("no-store = %v", noStore)
			}
			if b, _ := io.ReadAll(got.Body); !bytes.Equal(b, test.body) {
				t.Error("the body was not passed on intact")
			}
			if err := got.Body.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	tr := &partialTransport{next: staticTransport{err: errors.New("dial failed")}}
	if _, err := tr.RoundTrip(httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)); err == nil {
		t.Error("a transport error was lost")
	}
	base := &http.Transport{}
	hc := &http.Client{Transport: base}
	(&Client{}).detectPartialResponses(nil, hc)
	if pt, ok := hc.Transport.(*partialTransport); !ok || pt.next != base {
		t.Error("the backend's transport was not wrapped")
	}
	hc = &http.Client{}
	(&Client{}).detectPartialResponses(nil, hc)
	if hc.Transport != nil {
		t.Error("a client without its own transport was given one")
	}
}
