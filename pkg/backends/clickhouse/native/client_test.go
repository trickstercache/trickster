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
	"fmt"
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
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
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
	r := httptest.NewRequest(http.MethodPost, "/?default_format=JSON", strings.NewReader(sql))
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
	wg.Go(func() {
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
	})
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

func TestNativeTransportFormatsAndIdentity(t *testing.T) {
	requests := make(chan *http.Request, 8)
	addr, stop := startNativeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = io.WriteString(w, `{"meta":[{"name":"n","type":"UInt64"},{"name":"ts","type":"DateTime64(6)"}],"data":[{"n":"18446744073709551615","ts":"2020-01-01 00:00:00.123456"}],"rows":1}`)
	}))
	defer stop()
	c, err := NewNativeClient(&bo.Options{Host: addr, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, format := range []string{"JSON", "TSVWithNamesAndTypes", "Native"} {
		r := buildQueryRequest(t, "SELECT n, ts FROM events FORMAT "+format)
		r.URL.RawQuery = url.Values{"database": {"analytics"}, "max_threads": {"2"}, "query_id": {"test-query"}}.Encode()
		r.SetBasicAuth("alice", "secret")
		resp, err := c.Fetch(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", format, body)
		}
		if resp.Header.Get("X-ClickHouse-Format") != format {
			t.Fatalf("wrong format %v", resp.Header)
		}
		if format != "Native" && (!strings.Contains(string(body), "18446744073709551615") || !strings.Contains(string(body), ".123456")) {
			t.Fatalf("lost precision: %s", body)
		}
		if format == "Native" && body[0] != 2 {
			t.Fatalf("unexpected bare Native block: %x", body)
		}
		received := <-requests
		user, password, ok := received.BasicAuth()
		if !ok || user != "alice" || password != "secret" {
			t.Fatal("credentials were not forwarded")
		}
		if received.URL.Query().Get("database") != "analytics" || received.URL.Query().Get("max_threads") != "2" {
			t.Fatalf("missing settings: %s", received.URL)
		}
	}
	c.CloseIdleConnections()
	if _, err := c.pool(buildQueryRequest(t, "SELECT 1")); err == nil {
		t.Fatal("retired transport accepted a new query")
	}
}

func TestNativeTextValues(t *testing.T) {
	n := uint64(18446744073709551615)
	tm := time.Unix(1577836800, 123456000).UTC()
	for _, test := range []struct {
		value     any
		typ, want string
	}{
		{&n, "Nullable(UInt64)", "18446744073709551615"},
		{&tm, "Nullable(DateTime64(6))", "2020-01-01 00:00:00.123456"},
		{(*uint64)(nil), "Nullable(UInt64)", "\\N"},
	} {
		if got := textValue(test.value, test.typ); got != test.want {
			t.Fatalf("%s: got %q want %q", test.typ, got, test.want)
		}
	}
	if _, _, err := encodeResultWithSettings(
		[]server.Column{{Name: "a", Type: "Array(UInt32)"}}, [][]any{{[]uint32{1, 2}}}, 1, "TSV", "", nil,
	); err == nil {
		t.Fatal("accepted unsupported compound text format")
	}
}

func TestNativeClientConfiguration(t *testing.T) {
	if _, err := NewNativeClient(nil); err == nil {
		t.Fatal("accepted nil options")
	}
	if _, err := NewNativeClient(&bo.Options{OriginURL: "http://%"}); err == nil {
		t.Fatal("accepted invalid origin URL")
	}
	c, err := NewNativeClient(&bo.Options{
		OriginURL:          "https://alice:secret@localhost:9440/?database=analytics&max_threads=4",
		MaxConcurrentConns: 7,
		MaxIdleConns:       3,
		KeepAliveTimeout:   timeconv.Duration(2 * time.Second),
		Timeout:            timeconv.Duration(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.defaults != (sessionKey{database: "analytics", username: "alice", password: "secret"}) {
		t.Fatalf("defaults = %#v", c.defaults)
	}
	if c.settings["max_threads"] != "4" || c.options.TLS == nil {
		t.Fatalf("settings or TLS missing: %#v %#v", c.settings, c.options.TLS)
	}
	if c.db.Stats().MaxOpenConnections != 7 {
		t.Fatalf("max connections = %d", c.db.Stats().MaxOpenConnections)
	}
}

func TestNativeClientCredentialIsolation(t *testing.T) {
	c, err := NewNativeClient(&bo.Options{Host: "127.0.0.1:19000"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	request := buildQueryRequest(t, "SELECT 1")
	request.URL.RawQuery = "database=one&user=query-user&password=query-password"
	queryPool, err := c.pool(request)
	if err != nil {
		t.Fatal(err)
	}
	request = buildQueryRequest(t, "SELECT 1")
	request.Header.Set("X-ClickHouse-User", "header-user")
	request.Header.Set("X-ClickHouse-Key", "header-password")
	headerPool, err := c.pool(request)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("basic-user", "basic-password")
	basicPool, err := c.pool(request)
	if err != nil {
		t.Fatal(err)
	}
	if queryPool == headerPool || headerPool == basicPool || queryPool == basicPool {
		t.Fatal("credentials shared a native session pool")
	}
	if again, err := c.pool(request); err != nil || again != basicPool {
		t.Fatalf("identity pool was not reused: %p %v", again, err)
	}
	for i := len(c.pools); i < 64; i++ {
		r := buildQueryRequest(t, "SELECT 1")
		r.SetBasicAuth(fmt.Sprintf("user-%d", i), "password")
		if _, err := c.pool(r); err != nil {
			t.Fatal(err)
		}
	}
	request = buildQueryRequest(t, "SELECT 1")
	request.SetBasicAuth("one-too-many", "password")
	if _, err := c.pool(request); err == nil {
		t.Fatal("accepted more than 64 identity pools")
	}
}

func TestNativeResultTextEscaping(t *testing.T) {
	columns := []server.Column{
		{Name: "special\tname", Type: "String"},
		{Name: "nullable", Type: "Nullable(String)"},
	}
	values := [][]any{{"a\tb\nc\\d", "\\N"}, {nil, "value"}}
	got, _, err := encodeResultWithSettings(columns, values, 2, "TSVWithNamesAndTypes", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "special\\tname\tnullable\nString\tNullable(String)\na\\tb\\nc\\\\d\t\\N\n\\\\N\tvalue\n"
	if string(got) != want {
		t.Fatalf("TSV = %q, want %q", got, want)
	}
	got, _, err = encodeResultWithSettings(columns[:1], values[:1], 2, "CSVWithNames", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "special\tname\n\"a\tb\nc\\d\"\n\\N\n" {
		t.Fatalf("CSV = %q", got)
	}
	if _, _, err := encodeResultWithSettings(columns, values, 2, "Native", "invalid", nil); err == nil {
		t.Fatal("accepted invalid client protocol revision")
	}
	got, _, err = encodeResultWithSettings(columns, values, 2, "TSV", "", url.Values{
		"format_tsv_null_representation":     {"NULL"},
		"output_format_tsv_crlf_end_of_line": {"1"},
	})
	if err != nil || string(got) != "a\\tb\\nc\\\\d\tNULL\r\n\\\\N\tvalue\r\n" {
		t.Fatalf("configured TSV = %q, %v", got, err)
	}
	got, _, err = encodeResultWithSettings(columns[:1], values[:1], 2, "CSV", "", url.Values{
		"format_csv_delimiter":               {";"},
		"output_format_csv_crlf_end_of_line": {"true"},
	})
	if err != nil || string(got) != "\"a\tb\r\nc\\d\"\r\n\\N\r\n" {
		t.Fatalf("configured CSV = %q, %v", got, err)
	}
	if _, _, err := encodeResultWithSettings(columns, values, 2, "CSV", "", url.Values{
		"format_csv_delimiter": {"too long"},
	}); err == nil {
		t.Fatal("accepted an invalid CSV delimiter")
	}
}

func TestNativeRoundTripAndFormats(t *testing.T) {
	c, err := NewNativeClient(&bo.Options{Host: "127.0.0.1:19000"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	resp, err := c.RoundTrip(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("RoundTrip = %#v, %v", resp, err)
	}
	for _, format := range []string{
		"JSON", "Native", "CSV", "CSVWithNames", "TabSeparated", "TSV",
		"TabSeparatedWithNames", "TSVWithNames", "TabSeparatedWithNamesAndTypes", "TSVWithNamesAndTypes",
	} {
		if !supportedFormat(format) {
			t.Errorf("format %q is unsupported", format)
		}
	}
	if supportedFormat("Parquet") {
		t.Fatal("accepted unsupported format")
	}
	resp, err = c.Fetch(buildQueryRequest(t, "SELECT 1 FORMAT Parquet"))
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported format response = %#v, %v", resp, err)
	}
}
