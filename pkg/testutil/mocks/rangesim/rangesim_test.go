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

package rangesim

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseRangeHeader(t *testing.T) {
	br := parseRangeHeader("bytes=20-40, 0-10")
	if len(br) != 2 || br[0].end != 10 || br[1].start != 20 {
		t.Errorf("expected two ranges sorted by end, got %v", br)
	}
	if br := parseRangeHeader("bytes=500-"); len(br) != 1 || br[0].end != -1 {
		t.Errorf("expected an open-ended range, got %v", br)
	}
	for _, in := range []string{"", "bytes=", "items=0-10", "bytes=10", "bytes=a0-n0", "bytes=0-n0"} {
		if br := parseRangeHeader(in); br != nil {
			t.Errorf("%q: expected nil got %v", in, br)
		}
	}
}

func TestValidate(t *testing.T) {
	br := parseRangeHeader("bytes=0-10,20-40")
	if !br.validate(100) {
		t.Error("expected valid ranges")
	}
	br[1].start = 45
	if br.validate(100) {
		t.Error("expected invalid ranges")
	}
}

func TestWriteMultipartResponse(t *testing.T) {
	br := parseRangeHeader("bytes=0-10,20-40")
	var buf bytes.Buffer
	if err := br.writeMultipartResponse(100, &buf); err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(&buf, separator)
	for i, expected := range []string{Body[0:11], Body[20:41]} {
		p, err := mr.NextPart()
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		b, _ := io.ReadAll(p)
		if string(b) != expected {
			t.Errorf("part %d: expected %q got %q", i, expected, b)
		}
	}
}

func TestWriteRangeWrapsAroundBody(t *testing.T) {
	var buf bytes.Buffer
	writeRange(&buf, byteRange{start: contentLength - 10, end: contentLength + 9})
	if expected := (Body + Body)[contentLength-10 : contentLength+10]; buf.String() != expected {
		t.Errorf("expected %q got %q", expected, buf.String())
	}
}

func request(t *testing.T, target string, hdr http.Header) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+target, nil)
	maps.Copy(r.Header, hdr)
	mux := http.NewServeMux()
	Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Result()
}

func TestHandlerCustomizations(t *testing.T) {
	for _, q := range []string{"max-age=a", "max-age=0"} {
		if v := request(t, Path+"?"+q, nil).Header.Get(hnCacheControl); v != "" {
			t.Errorf("%s: expected no Cache-Control, got %s", q, v)
		}
	}
	if v := request(t, Path+"?max-age=5", nil).Header.Get(hnCacheControl); v != "max-age=5" {
		t.Errorf("expected max-age=5 got %s", v)
	}
	cases := []struct {
		query    string
		hdr      http.Header
		expected int
	}{
		{"status=404", nil, http.StatusNotFound},
		{"status=412", nil, http.StatusRequestedRangeNotSatisfiable},
		{"status=200", http.Header{hnRange: {"bytes=0-10"}}, http.StatusOK},
		{"non-ims=200", http.Header{hnRange: {"bytes=0-10"}}, http.StatusOK},
		{"non-ims=500", nil, http.StatusInternalServerError},
		{"ims=200", http.Header{hnIfModifiedSince: {"trickster"}}, http.StatusOK},
		{"ims=404", http.Header{hnIfModifiedSince: {"trickster"}}, http.StatusNotFound},
	}
	for _, c := range cases {
		if code := request(t, Path+"?"+c.query, c.hdr).StatusCode; code != c.expected {
			t.Errorf("%s: expected %d got %d", c.query, c.expected, code)
		}
	}
}

func TestHandler(t *testing.T) {
	res := request(t, Path+"any/path?max-age=1", nil)
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(b) != Body {
		t.Errorf("expected 200 and the full body, got %d with %d bytes", res.StatusCode, len(b))
	}
	if v := res.Header.Get(hnLastModified); v != "Wed, 01 Jan 2020 00:00:00 UTC" {
		t.Errorf("unexpected Last-Modified %s", v)
	}

	res = request(t, Path+"a", http.Header{hnRange: {"bytes=0-10"}})
	b, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusPartialContent || string(b) != Body[:11] {
		t.Errorf("expected 206 with the first 11 bytes, got %d %q", res.StatusCode, b)
	}
	if v := res.Header.Get(hnContentRange); v != "bytes 0-10/1224" {
		t.Errorf("unexpected Content-Range %s", v)
	}

	res = request(t, Path+"a", http.Header{hnRange: {"bytes=0-10,20-30"}})
	if mt, _, _ := mime.ParseMediaType(res.Header.Get(hnContentType)); res.StatusCode != http.StatusPartialContent ||
		mt != "multipart/byteranges" {
		t.Errorf("expected a 206 multipart response, got %d %s", res.StatusCode, mt)
	}

	for _, rng := range []string{"bytes=40-30", "bytes=99999999-99999999"} {
		res = request(t, Path+"a", http.Header{hnRange: {rng}})
		if res.StatusCode != http.StatusRequestedRangeNotSatisfiable || res.Header.Get(hnContentType) != "" {
			t.Errorf("%s: expected a bare 416, got %d", rng, res.StatusCode)
		}
	}

	res = request(t, Path+"a", http.Header{hnIfModifiedSince: {time.Unix(1577836799, 0).UTC().Format(time.RFC1123)}})
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for a stale If-Modified-Since, got %d", res.StatusCode)
	}
	res = request(t, Path+"a", http.Header{hnIfModifiedSince: {time.Unix(1577836801, 0).UTC().Format(time.RFC1123)}})
	if res.StatusCode != http.StatusNotModified {
		t.Errorf("expected 304, got %d", res.StatusCode)
	}
}

func TestHandlerSize(t *testing.T) {
	res := request(t, Path+"a?size=3000", nil)
	b, _ := io.ReadAll(res.Body)
	if len(b) != 3000 || !strings.HasPrefix(string(b), Body+Body) {
		t.Errorf("expected 3000 bytes of repeated Body, got %d", len(b))
	}
	res = request(t, Path+"a?size=3000", http.Header{hnRange: {"bytes=1200-1300"}})
	b, _ = io.ReadAll(res.Body)
	if expected := (Body + Body)[1200:1301]; res.StatusCode != http.StatusPartialContent || string(b) != expected {
		t.Errorf("expected 206 wrapping around Body, got %d %q", res.StatusCode, b)
	}
}

type failingWriter struct {
	header http.Header
	writes int
}

func (f *failingWriter) Header() http.Header { return f.header }
func (f *failingWriter) WriteHeader(int)     {}
func (f *failingWriter) Write([]byte) (int, error) {
	f.writes++
	return 0, errors.New("client went away")
}

func TestHandlerStopsWhenWritesFail(t *testing.T) {
	for _, hdr := range []http.Header{{}, {hnRange: {"bytes=0-1073741822"}}, {hnRange: {"bytes=0-10,20-1073741822"}}} {
		w := &failingWriter{header: http.Header{}}
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+Path+"a?size=1073741824", nil)
		maps.Copy(r.Header, hdr)
		handler(w, r)
		if w.writes > 2 {
			t.Errorf("%v: expected the response to stop at the first failed write, got %d writes", hdr, w.writes)
		}
	}
	if err := writeRange(&failingWriter{}, byteRange{start: 0, end: 1 << 40}); err == nil {
		t.Error("expected writeRange to return the write error")
	}
}

func TestHandlerLimits(t *testing.T) {
	if code := request(t, Path+"a?size=1073741825", nil).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for a size over MaxSize, got %d", code)
	}
	ranges := strings.Repeat("0-0,", maxRanges-1) + "0-0"
	if code := request(t, Path+"a", http.Header{hnRange: {"bytes=" + ranges}}).StatusCode; code != http.StatusPartialContent {
		t.Errorf("expected 206 at the range limit, got %d", code)
	}
	res := request(t, Path+"a", http.Header{hnRange: {"bytes=" + ranges + ",0-0"}})
	if b, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK || string(b) != Body {
		t.Errorf("expected the full body past the range limit, got %d", res.StatusCode)
	}
	if code := request(t, Path+"a", http.Header{hnRange: {"bytes=0-9223372036854775807"}}).StatusCode; code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("expected 416 for a range ending at MaxInt64, got %d", code)
	}
}

type countingWriter struct {
	header http.Header
	code   int
	n      int64
}

func (c *countingWriter) Header() http.Header  { return c.header }
func (c *countingWriter) WriteHeader(code int) { c.code = code }
func (c *countingWriter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	c.n += int64(len(b))
	return len(b), nil
}

func TestOverlappingRangesCannotMultiplyMaxSize(t *testing.T) {
	serveCounted := func(rng string) *countingWriter {
		w := &countingWriter{header: http.Header{}}
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+Path+"a?size=1073741824", nil)
		r.Header.Set(hnRange, byteRequestRangePrefix+rng)
		handler(w, r)
		return w
	}
	full := "0-1073741823"
	w := serveCounted(strings.Repeat(full+",", maxRanges-1) + full)
	if w.code != http.StatusOK || w.n != MaxSize {
		t.Errorf("expected the %d-byte full body for overlapping ranges, got %d with %d bytes", MaxSize, w.code, w.n)
	}
	w = serveCounted("0-536870911,536870912-1073741823")
	if w.code != http.StatusPartialContent || w.n <= MaxSize {
		t.Errorf("expected a 206 for ranges totaling MaxSize, got %d with %d bytes", w.code, w.n)
	}
}
