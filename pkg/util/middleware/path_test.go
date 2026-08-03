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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripPathPrefix(t *testing.T) {
	tests := []struct {
		name         string
		prefix       string
		requestPath  string
		expectedPath string
	}{
		{
			name:         "strips fully qualified prefix",
			prefix:       "/backend/",
			requestPath:  "/backend/api/v1/query",
			expectedPath: "/api/v1/query",
		},
		{
			name:         "normalizes missing leading slash",
			prefix:       "backend/",
			requestPath:  "/backend/api/v1/query",
			expectedPath: "/api/v1/query",
		},
		{
			name:         "normalizes missing trailing slash",
			prefix:       "/backend",
			requestPath:  "/backend/api/v1/query",
			expectedPath: "/api/v1/query",
		},
		{
			name:         "normalizes missing leading and trailing slash",
			prefix:       "backend",
			requestPath:  "/backend/api/v1/query",
			expectedPath: "/api/v1/query",
		},
		{
			name:         "leaves root of stripped prefix",
			prefix:       "/backend/",
			requestPath:  "/backend/",
			expectedPath: "/",
		},
		{
			name:         "does not strip when path lacks trailing slash after prefix",
			prefix:       "/backend/",
			requestPath:  "/backend",
			expectedPath: "/backend",
		},
		{
			name:         "does not strip unrelated path",
			prefix:       "/backend/",
			requestPath:  "/other/api/v1/query",
			expectedPath: "/other/api/v1/query",
		},
		{
			name:         "does not strip partial name match",
			prefix:       "/backend/",
			requestPath:  "/backend-extra/api",
			expectedPath: "/backend-extra/api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			})

			r := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			w := httptest.NewRecorder()
			StripPathPrefix(tt.prefix, next).ServeHTTP(w, r)

			require.Equal(t, http.StatusNoContent, w.Code)
			require.Equal(t, tt.expectedPath, gotPath)
		})
	}
}

func TestStripPathPrefixPreservesQuery(t *testing.T) {
	var gotQuery string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest(http.MethodGet, "/backend/api?query=up&step=60", nil)
	w := httptest.NewRecorder()
	StripPathPrefix("/backend/", next).ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "query=up&step=60", gotQuery)
	require.Equal(t, "/api", r.URL.Path)
}
