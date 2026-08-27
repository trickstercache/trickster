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

package response

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

func TestWriteResponseHeaderNonResponseWriter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := WriteResponseHeader(&buf, http.StatusTeapot, 0, http.Header{
		"X-Test": []string{"value"},
	})
	require.NoError(t, err)
	require.Empty(t, buf.Bytes())
}

func TestWriteResponseHeaderDefaultsStatusOK(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	err := WriteResponseHeader(w, 0, 0, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, headers.ValueApplicationJSON+"; charset=UTF-8",
		w.Header().Get(headers.NameContentType))
}

func TestWriteResponseHeaderContentTypeHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hint     byte
		wantType string
	}{
		{
			name:     "json hint 0",
			hint:     0,
			wantType: headers.ValueApplicationJSON + "; charset=UTF-8",
		},
		{
			name:     "json hint 1",
			hint:     1,
			wantType: headers.ValueApplicationJSON + "; charset=UTF-8",
		},
		{
			name:     "csv hint 2",
			hint:     2,
			wantType: headers.ValueApplicationCSV + "; charset=UTF-8",
		},
		{
			name:     "unknown hint",
			hint:     99,
			wantType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			err := WriteResponseHeader(w, http.StatusCreated, tt.hint, nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusCreated, w.Code)
			require.Equal(t, tt.wantType, w.Header().Get(headers.NameContentType))
		})
	}
}

func TestWriteResponseHeaderCopiesHeaders(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	h := http.Header{
		"X-Custom":      []string{"first", "second"},
		"X-Empty":       []string{},
		"Cache-Control": []string{"no-cache"},
	}

	err := WriteResponseHeader(w, http.StatusAccepted, 2, h)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, "first", w.Header().Get("X-Custom"))
	require.Empty(t, w.Header().Values("X-Empty"))
	require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	require.Equal(t, headers.ValueApplicationCSV+"; charset=UTF-8",
		w.Header().Get(headers.NameContentType))
}

func TestWriteResponseHeaderHeaderOverridesContentTypeHint(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	h := http.Header{
		headers.NameContentType: []string{"text/plain"},
	}

	err := WriteResponseHeader(w, http.StatusOK, 0, h)
	require.NoError(t, err)
	require.Equal(t, "text/plain", w.Header().Get(headers.NameContentType))
}
