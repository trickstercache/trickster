/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package rangesim

import (
	"github.com/Comcast/trickster/internal/proxy/headers"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewTestServer(t *testing.T) {
	if NewTestServer() == nil {
		t.Errorf("failed to get test server object")
	}
}

func TestWriteError(t *testing.T) {

	w := httptest.NewRecorder()
	writeError(http.StatusNotFound, []byte("Not Found"), w)

	r := w.Result()
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("expected %d got %d", http.StatusNotFound, r.StatusCode)
	}

}

func TestHandlerCustomizations(t *testing.T) {

	// test invalid max age
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/?max-age=a", nil)
	w := httptest.NewRecorder()
	handler(w, r)

	if v := w.Header().Get(hnCacheControl); v != "" {
		t.Errorf("expected %s got %s", "", v)
	}

	// test max age 0
	r, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/?max-age=0", nil)
	w = httptest.NewRecorder()
	handler(w, r)

	if v := w.Header().Get(hnCacheControl); v != "" {
		t.Errorf("expected %s got %s", "", v)
	}

	// test custom status code of 404
	r, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/?status=404", nil)
	w = httptest.NewRecorder()
	handler(w, r)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Errorf("expected %d got %d", http.StatusNotFound, w.Result().StatusCode)
	}

	// test custom status code of 200
	r, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/?status=200", nil)
	w = httptest.NewRecorder()
	handler(w, r)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, w.Result().StatusCode)
	}

	// test custom non-ims code of 200
	r, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/?non-ims=200", nil)
	w = httptest.NewRecorder()
	handler(w, r)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, w.Result().StatusCode)
	}

	// test custom ims code of 200
	r, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/?ims=200", nil)
	r.Header.Set("If-Modified-Since", "trickster")
	w = httptest.NewRecorder()
	handler(w, r)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, w.Result().StatusCode)
	}

}

func TestHandler(t *testing.T) {

	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/?max-age=1", nil)
	w := httptest.NewRecorder()
	handler(w, r)
	res := w.Result()

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, res.StatusCode)
	}

	h := make(http.Header)
	h.Set(headers.NameRange, "bytes=0-10")
	r.Header = h
	w = httptest.NewRecorder()
	handler(w, r)
	res = w.Result()
	rh := res.Header

	if res.StatusCode != http.StatusPartialContent {
		t.Errorf("expected %d got %d", http.StatusPartialContent, res.StatusCode)
	}

	if v := rh.Get(headers.NameContentType); !strings.HasPrefix(v, headers.ValueTextPlain) {
		t.Errorf("expected %s got %s", headers.ValueTextPlain, v)
	}

	h.Set(headers.NameRange, "bytes=0-10,20-30")
	w = httptest.NewRecorder()
	handler(w, r)
	res = w.Result()
	rh = res.Header

	if res.StatusCode != http.StatusPartialContent {
		t.Errorf("expected %d got %d", http.StatusPartialContent, res.StatusCode)
	}

	if v := rh.Get(headers.NameContentType); !strings.HasPrefix(v, headers.ValueMultipartByteRanges) {
		t.Errorf("expected %s got %s", headers.ValueMultipartByteRanges, v)
	}

	// test bad range
	h.Set(headers.NameRange, "bytes=40-30")
	w = httptest.NewRecorder()
	handler(w, r)
	res = w.Result()
	rh = res.Header

	if res.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("expected %d got %d", http.StatusRequestedRangeNotSatisfiable, res.StatusCode)
	}

	if v := rh.Get(headers.NameContentType); v != "" {
		t.Errorf("expected empty string got %s", v)
	}

	h.Del(headers.NameRange)
	h.Set(headers.NameIfModifiedSince, time.Unix(1577836799, 0).Format(time.RFC1123))
	w = httptest.NewRecorder()
	handler(w, r)
	res = w.Result()
	rh = res.Header

	if res.StatusCode != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, res.StatusCode)
	}

	if v := rh.Get(headers.NameContentType); !strings.HasPrefix(v, headers.ValueTextPlain) {
		t.Errorf("expected %s got %s", headers.ValueTextPlain, v)
	}

	h.Set(headers.NameIfModifiedSince, time.Unix(1577836801, 0).Format(time.RFC1123))
	w = httptest.NewRecorder()
	handler(w, r)
	res = w.Result()
	rh = res.Header

	if res.StatusCode != http.StatusNotModified {
		t.Errorf("expected %d got %d", http.StatusNotModified, res.StatusCode)
	}

	if v := rh.Get(headers.NameContentType); v != "" {
		t.Errorf("expected empty string got %s", v)
	}

}
