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

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"

	"github.com/stretchr/testify/require"
)

func TestQueryAsPost(t *testing.T) {
	var seenMethod, seenBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.WriteHeader(http.StatusOK)
	})
	accepted := []string{headers.ValueXFormURLEncoded, headers.ValueApplicationJSON}
	const wantAcceptQuery = `"application/x-www-form-urlencoded", "application/json"`
	h := QueryAsPost(accepted, next)

	serve := func(method, ct, body string) (*httptest.ResponseRecorder, *http.Request) {
		r := httptest.NewRequest(method, "/api/v1/query", strings.NewReader(body))
		if ct != "" {
			r.Header.Set(headers.NameContentType, ct)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w, r
	}

	t.Run("other methods pass through and see the advertisement", func(t *testing.T) {
		seenMethod = ""
		w, _ := serve(http.MethodGet, "", "")
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, http.MethodGet, seenMethod)
		require.Equal(t, wantAcceptQuery, w.Header().Get(headers.NameAcceptQuery))
	})

	t.Run("an accepted QUERY is forwarded as POST", func(t *testing.T) {
		seenMethod = ""
		w, r := serve(methods.MethodQuery, "Application/X-WWW-Form-URLEncoded; charset=utf-8", "query=up")
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, http.MethodPost, seenMethod)
		require.Equal(t, "query=up", seenBody)
		require.Equal(t, methods.MethodQuery, r.Method, "the client's request keeps its method")
	})

	t.Run("a QUERY without a media type is rejected", func(t *testing.T) {
		seenMethod = ""
		w, _ := serve(methods.MethodQuery, "", "query=up")
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Empty(t, seenMethod)
	})

	t.Run("a QUERY with an unsupported media type is rejected", func(t *testing.T) {
		seenMethod = ""
		w, _ := serve(methods.MethodQuery, headers.ValueTextPlain, "up")
		require.Equal(t, http.StatusUnsupportedMediaType, w.Code)
		require.Equal(t, wantAcceptQuery, w.Header().Get(headers.NameAcceptQuery))
		require.Empty(t, seenMethod)
	})
}

func TestQueryAsPostWithoutMediaTypes(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h := QueryAsPost(nil, next)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(methods.MethodQuery, "/", nil))
	require.Empty(t, w.Header().Get(headers.NameAcceptQuery))
}

func TestFormatAcceptQuery(t *testing.T) {
	require.Empty(t, FormatAcceptQuery(nil))
	require.Equal(t, `"application/sql"`, FormatAcceptQuery([]string{headers.ValueApplicationSQL}))
}
