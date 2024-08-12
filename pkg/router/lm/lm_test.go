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

package lm

import (
	"net/http"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/testutil/writer"
)

const testPathExact1 = "/path/exact"
const testPathExact2 = "/path/exact/2"
const testPathPrefix1 = "/path/prefix"
const testPathPrefix2 = "/path/prefix/2"

func TestRegisterRoute(t *testing.T) {

	const testPathExact1 = "/path1/exact"

	r := NewRouter().(*lmRouter)
	r.RegisterRoute(testPathExact1, nil, nil, false, notFoundHandler)

	hrs, ok := r.routes[""]
	if !ok || hrs == nil {
		t.Fatal("expected non-nil route set")
	}
	rll, ok := hrs.ExactMatchRoutes[testPathExact1]
	if !ok || rll == nil {
		t.Fatal("expected non-nil route lookup")
	}

	err := r.RegisterRoute("", nil, nil, false, notFoundHandler)
	if err != errors.ErrInvalidPath {
		t.Fatal("expected error for invalid path")
	}

	err = r.RegisterRoute(testPathPrefix1, nil, []string{"invalidMethod"},
		false, notFoundHandler)
	if err != errors.ErrInvalidMethod {
		t.Fatal("expected error for invalid method")
	}

	err = r.RegisterRoute(testPathPrefix1, nil, []string{http.MethodGet},
		true, notFoundHandler)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHandler(t *testing.T) {
	r := NewRouter().(*lmRouter)
	r.RegisterRoute(testPathExact1, nil, nil, false, testResponse1Handler)
	r.RegisterRoute(testPathPrefix2, []string{"example.com"}, nil, true,
		testResponse2Handler)
	r.RegisterRoute(testPathPrefix1, []string{"example.com"}, nil, true,
		testResponse1Handler)

	req, _ := http.NewRequest(http.MethodGet, testPathExact1, nil)
	req.Host = "example.com:8080"
	h := r.Handler(req)
	w := writer.NewWriter().(*writer.TestResponseWriter)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok := serveAndVerifyTestResponse1(h, w, req)
	if !ok {
		t.Fatal("expected test response 1 handler")
	}

	// POST request should fail with Method Not Allowed
	req, _ = http.NewRequest(http.MethodPost, testPathExact1, nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyMethodNotAllowed(h, w, req)
	if !ok {
		t.Fatal("expected method not allowed handler")
	}

	// request should fail with 404 Not Found
	req, _ = http.NewRequest(http.MethodPost, testPathExact2, nil)
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyNotFound(h, w, req)
	if !ok {
		t.Fatal("expected 404 not found handler")
	}

	// Prefix Route 1 should pass with test response 1
	req, _ = http.NewRequest(http.MethodGet, testPathPrefix1+"/more/path", nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = serveAndVerifyTestResponse1(h, w, req)
	if !ok {
		t.Fatal("expected test response 1 handler")
	}

	// POST on Prefix Route 1 should fail with Method Not Allowed
	req, _ = http.NewRequest(http.MethodPost, testPathPrefix1+"/more/path", nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyMethodNotAllowed(h, w, req)
	if !ok {
		t.Fatal("expected method not allowed handler")
	}

	r.RegisterRoute(testPathExact2, []string{"example.com"}, nil, false,
		testResponse2Handler)
	req, _ = http.NewRequest(http.MethodGet, testPathExact2, nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyTestResponse2(h, w, req)
	if !ok {
		t.Fatal("expected test response 2 handler")
	}

	r.SetMatchingScheme(0)
	req, _ = http.NewRequest(http.MethodConnect, testPathPrefix1, nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyNotFound(h, w, req)
	if !ok {
		t.Fatal("expected 404 not found handler")
	}

}

func TestServeHTTP(t *testing.T) {
	r := NewRouter().(*lmRouter)
	r.RegisterRoute("/", nil, nil, true, testResponse1Handler)
	w := writer.NewWriter().(*writer.TestResponseWriter)
	req, _ := http.NewRequest(http.MethodGet, testPathPrefix1, nil)
	req.RequestURI = "*"
	r.ServeHTTP(w, req)
	ok := verifyBadRequest(w)
	if !ok {
		t.Fatal("expected 400 bad request handler")
	}
	req, _ = http.NewRequest(http.MethodGet, testPathPrefix1, nil)
	w.Reset()
	r.ServeHTTP(w, req)
	ok = verifyTestResponse1(w)
	if !ok {
		t.Fatal("expected test response 1 handler")
	}
}

func verifyNotFound(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request) bool {
	h.ServeHTTP(w, r)
	return w.StatusCode == http.StatusNotFound
}

func verifyMethodNotAllowed(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request) bool {
	h.ServeHTTP(w, r)
	return w.StatusCode == http.StatusMethodNotAllowed
}

const testResponse1Text = "test response 1"
const testResponse2Text = "test response 2"

func testResponse1(w http.ResponseWriter, r *http.Request) {
	http.Error(w, testResponse1Text, http.StatusOK)
}

var testResponse1Handler = http.HandlerFunc(testResponse1)

func serveAndVerifyTestResponse1(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request) bool {
	h.ServeHTTP(w, r)
	return verifyTestResponse1(w)
}

func verifyTestResponse1(w *writer.TestResponseWriter) bool {
	return w.StatusCode == http.StatusOK &&
		strings.TrimSpace(string(w.Bytes)) == testResponse1Text
}

func testResponse2(w http.ResponseWriter, r *http.Request) {
	http.Error(w, testResponse2Text, http.StatusOK)
}

var testResponse2Handler = http.HandlerFunc(testResponse2)

func verifyTestResponse2(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request) bool {
	h.ServeHTTP(w, r)
	return w.StatusCode == http.StatusOK &&
		strings.TrimSpace(string(w.Bytes)) == testResponse2Text
}

func verifyBadRequest(w *writer.TestResponseWriter) bool {
	return w.StatusCode == http.StatusBadRequest
}
