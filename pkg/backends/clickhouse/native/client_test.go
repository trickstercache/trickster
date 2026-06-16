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

package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

func TestNewNativeClient(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"empty host", "", true},
		{"host with port", "localhost:9000", false},
		{"host without port", "localhost", false},
		{"ipv4 with port", "127.0.0.1:19000", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &bo.Options{Host: tc.host}
			c, err := NewNativeClient(o)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c == nil || c.db == nil {
				t.Fatal("expected non-nil client with non-nil db")
			}
			if err := c.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	c, err := NewNativeClient(&bo.Options{Host: "127.0.0.1:19000"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestExtractSQL(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		body    string
		query   string
		want    string
		wantErr bool
	}{
		{
			name:   "post body",
			method: http.MethodPost,
			body:   "SELECT 1",
			want:   "SELECT 1",
		},
		{
			name:   "get query param",
			method: http.MethodGet,
			query:  "SELECT 2",
			want:   "SELECT 2",
		},
		{
			name:   "post body preferred over query param",
			method: http.MethodPost,
			body:   "SELECT body",
			query:  "SELECT param",
			want:   "SELECT body",
		},
		{
			name:    "no sql",
			method:  http.MethodGet,
			wantErr: true,
		},
		{
			name:    "empty post body falls back to missing",
			method:  http.MethodPost,
			wantErr: true,
		},
		{
			name:   "empty post body falls back to query",
			method: http.MethodPost,
			query:  "SELECT fallback",
			want:   "SELECT fallback",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := &url.URL{Path: "/"}
			if tc.query != "" {
				q := u.Query()
				q.Set("query", tc.query)
				u.RawQuery = q.Encode()
			}
			var body io.ReadCloser
			if tc.body != "" {
				body = io.NopCloser(strings.NewReader(tc.body))
			}
			r := &http.Request{Method: tc.method, URL: u, Body: body}
			got, err := extractSQL(r)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSyntheticErrorResponse(t *testing.T) {
	err := errors.New("boom")
	resp := syntheticErrorResponse(http.StatusBadRequest, err)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if resp.Status != "400 Bad Request" {
		t.Fatalf("status text: got %q", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("content-type: got %q", ct)
	}
	b, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(b) != "boom" {
		t.Fatalf("body: got %q", string(b))
	}
	if resp.ContentLength != int64(len("boom")) {
		t.Fatalf("length: got %d", resp.ContentLength)
	}
}

func TestFetchExtractError(t *testing.T) {
	c, err := NewNativeClient(&bo.Options{Host: "127.0.0.1:19000"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()

	r := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}}
	resp, err := c.Fetch(r)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestFetchQueryError(t *testing.T) {
	addr, stop := startNativeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad query", http.StatusBadRequest)
	}))
	defer stop()

	c, err := NewNativeClient(&bo.Options{Host: addr})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()

	r := buildQueryRequest(t, "SELECT 1")
	resp, err := c.Fetch(r)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

func TestFetchSuccess(t *testing.T) {
	addr, stop := startNativeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"meta": []map[string]string{
				{"name": "a", "type": "String"},
				{"name": "b", "type": "String"},
			},
			"data": []map[string]any{},
			"rows": 0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer stop()

	c, err := NewNativeClient(&bo.Options{Host: addr})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()

	r := buildQueryRequest(t, "SELECT a, b FROM t")
	resp, err := c.Fetch(r)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d body=%q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q", ct)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Meta []map[string]string `json:"meta"`
		Data []map[string]any    `json:"data"`
		Rows int                 `json:"rows"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Rows != 0 {
		t.Fatalf("rows: got %d", out.Rows)
	}
}

func TestFetchSuccessWithRows(t *testing.T) {
	addr, stop := startNativeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"meta": []map[string]string{
				{"name": "id", "type": "Int64"},
				{"name": "label", "type": "String"},
			},
			"data": []map[string]any{
				{"id": 1, "label": "alpha"},
				{"id": 2, "label": "beta"},
				{"id": 3, "label": "gamma"},
			},
			"rows": 3,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer stop()

	c, err := NewNativeClient(&bo.Options{Host: addr})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()

	r := buildQueryRequest(t, "SELECT id, label FROM t")
	resp, err := c.Fetch(r)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d body=%q", resp.StatusCode, body)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Meta []map[string]string `json:"meta"`
		Data []map[string]any    `json:"data"`
		Rows int                 `json:"rows"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Rows != 3 {
		t.Fatalf("rows: got %d, want 3", out.Rows)
	}
	if len(out.Data) != 3 {
		t.Fatalf("data len: got %d, want 3", len(out.Data))
	}
	wantLabels := []string{"alpha", "beta", "gamma"}
	for i, row := range out.Data {
		id, ok := row["id"].(float64)
		if !ok {
			t.Fatalf("row %d id: got %T %v", i, row["id"], row["id"])
		}
		if int64(id) != int64(i+1) {
			t.Fatalf("row %d id: got %v, want %d", i, id, i+1)
		}
		label, ok := row["label"].(string)
		if !ok {
			t.Fatalf("row %d label: got %T %v", i, row["label"], row["label"])
		}
		if label != wantLabels[i] {
			t.Fatalf("row %d label: got %q, want %q", i, label, wantLabels[i])
		}
	}
}

func buildQueryRequest(t *testing.T, sql string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(sql))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return r.WithContext(ctx)
}

func startNativeServer(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h := &server.Handler{QueryHandler: handler}
	ctx, cancel := context.WithCancel(context.Background())
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		conns []net.Conn
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				_ = h.HandleConnection(ctx, c)
			}(conn)
		}
	}()
	stop := func() {
		cancel()
		lis.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	}
	return lis.Addr().String(), stop
}
