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

package chaos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBehaviorOK(t *testing.T) {
	srv := httptest.NewServer(BehaviorOK(`{"status":"success","data":[]}`))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Equal(t, `{"status":"success","data":[]}`, string(body))
}

func TestBehaviorStatus(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusBadGateway} {
		srv := httptest.NewServer(BehaviorStatus(code))
		t.Cleanup(srv.Close)
		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equalf(t, code, resp.StatusCode, "BehaviorStatus(%d) returned %d", code, resp.StatusCode)
		require.Emptyf(t, body, "BehaviorStatus(%d) body should be empty, got %q", code, body)
	}
}

func TestBehaviorTruncateStaleCL(t *testing.T) {
	const stale, actual = 1024, 32
	srv := httptest.NewServer(BehaviorTruncateStaleCL(stale, actual))
	t.Cleanup(srv.Close)

	// Use a short-deadline transport so the over-read attempt doesn't hang the
	// test if Go's net/http happily accepts the truncated body.
	cli := &http.Client{Timeout: 2 * time.Second}
	resp, err := cli.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "1024", resp.Header.Get("Content-Length"))

	// We expect the body read to error (unexpected EOF) because the server
	// promised 1024 bytes and delivered 32. Either ReadAll returns
	// io.ErrUnexpectedEOF or returns fewer bytes than declared.
	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		require.Lessf(t, len(body), stale, "expected truncation; read full %d bytes", len(body))
	} else {
		require.ErrorIs(t, readErr, io.ErrUnexpectedEOF, "expected EOF-style truncation error, got %v", readErr)
	}
}

func TestBehaviorPanic(t *testing.T) {
	srv := httptest.NewServer(BehaviorPanic())
	t.Cleanup(srv.Close)
	// net/http recovers the handler panic but tears the connection down
	// before flushing a response, so the client observes an EOF or a
	// connection-reset rather than a 500. Both are acceptable signals
	// that the handler did not produce a normal response.
	resp, err := http.Get(srv.URL)
	if err != nil {
		assert.Error(t, err, "expected transport-level failure on panic")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.NotEqualf(t, http.StatusOK, resp.StatusCode,
		"panicking handler must not produce a 200 (got body=%q)", body)
}

func TestBehavior5xxWithLM(t *testing.T) {
	lm := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	srv := httptest.NewServer(Behavior5xxWithLM(http.StatusBadGateway, lm))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.Equal(t, lm.Format(http.TimeFormat), resp.Header.Get("Last-Modified"))
}

func TestBehaviorReturnsWarnings(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[]}}`
	srv := httptest.NewServer(BehaviorReturnsWarnings(body, "missing label foo", "scrape skipped"))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	var got struct {
		Status   string          `json:"status"`
		Data     json.RawMessage `json:"data"`
		Warnings []string        `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "success", got.Status)
	require.Equal(t, []string{"missing label foo", "scrape skipped"}, got.Warnings)
	require.True(t, strings.HasPrefix(string(got.Data), `{"resultType":"vector"`),
		"data should be passed through unchanged; got %s", string(got.Data))
}

func TestBehaviorReturnsWarningsFallback(t *testing.T) {
	srv := httptest.NewServer(BehaviorReturnsWarnings(`[1,2,3]`, "w1"))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var got struct {
		Status   string          `json:"status"`
		Data     json.RawMessage `json:"data"`
		Warnings []string        `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "success", got.Status)
	require.Equal(t, "[1,2,3]", string(got.Data))
	require.Equal(t, []string{"w1"}, got.Warnings)
}

func TestBehaviorSlowProbe(t *testing.T) {
	const d = 200 * time.Millisecond
	srv := httptest.NewServer(BehaviorSlowProbe(d))
	t.Cleanup(srv.Close)

	t.Run("waits and returns 200", func(t *testing.T) {
		start := time.Now()
		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.GreaterOrEqual(t, time.Since(start), d, "should have slept at least %s", d)
	})

	t.Run("honors client cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		require.NoError(t, err)
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		assert.Error(t, err, "expected context cancellation error")
		require.Less(t, time.Since(start), d, "should return well before %s on cancel", d)
	})
}
