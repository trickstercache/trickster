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

package bodyfilter

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/stretchr/testify/require"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read fail") }
func (errReader) Close() error             { return nil }

func TestHandler(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	t.Run("non body methods pass through", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
			t.Run(method, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/", strings.NewReader("payload"))
				Handler(1, false, next).ServeHTTP(rec, req)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, "ok", rec.Body.String())
			})
		}
	})

	t.Run("within limit", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
			t.Run(method, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/", strings.NewReader("hi"))
				Handler(10, false, next).ServeHTTP(rec, req)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, "ok", rec.Body.String())
			})
		}
	})

	t.Run("too large reject", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("too-large"))
		Handler(3, false, next).ServeHTTP(rec, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("too large truncate", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("too-large"))
		Handler(3, true, next).ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "ok", rec.Body.String())
	})

	t.Run("read error", func(t *testing.T) {
		for _, truncateOnly := range []bool{false, true} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", errReader{})
			Handler(10, truncateOnly, next).ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		}
	})

	t.Run("cached body too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
		rsc.RequestBody = []byte("cached-too-large")
		req = request.SetResources(req, rsc)
		Handler(4, false, next).ServeHTTP(rec, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.Contains(t, rec.Body.String(), failures.PayloadTooLargeText)
	})

	t.Run("cached body too large truncate", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
		rsc.RequestBody = []byte("1234-too-large")
		req = request.SetResources(req, rsc)
		verify := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			require.Equal(t, "1234", string(b), "body not truncated")
			require.Equal(t, []byte("1234"), request.GetResources(r).RequestBody, "cache not truncated")
			require.Equal(t, int64(4), r.ContentLength)
			require.Equal(t, "4", r.Header.Get("Content-Length"))
			w.WriteHeader(http.StatusOK)
		})
		Handler(4, true, verify).ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestHandlerNilBody(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = nil
	Handler(10, false, next).ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

var _ io.ReadCloser = errReader{}
