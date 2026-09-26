/*
 * Copyright 2026 The Trickster Authors
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

package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const metadata = "SOURCE_ROWS=2\nSOURCE_PICKUP_MIN_EPOCH=1704067200\nSOURCE_PICKUP_MAX_EPOCH=1704067202\n" +
	"SOURCE_DROPOFF_MIN_EPOCH=1704067260\nSOURCE_DROPOFF_MAX_EPOCH=1704067262\nSEED_EPOCH=1704153601\nSHIFT_SECONDS=86400\n"

func fixture() []string {
	r := make([]string, len(columns))
	for i, c := range columns {
		switch c.kind {
		case "DATE":
			r[i] = "2024-01-01"
		case "TIMESTAMP(6)":
			r[i] = "2024-01-01 00:00:00"
		case "STRING":
			r[i] = ""
		default:
			r[i] = "1"
		}
	}
	r[1], r[26], r[5] = "O'Brien\\fleet", "yellow", "2024-01-01 00:01:00"
	return r
}

func TestRowSQL(t *testing.T) {
	r := fixture()
	r[12] = ""
	got, err := rowSQL(r, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'O''Brien\\fleet'", "'2023-12-31','2023-12-31T23:59:59Z'", "'2024-01-01','2024-01-01T00:00:59Z'", ",NULL,", "1704067199)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	r[5] = ""
	got, err = rowSQL(r, 0)
	if err != nil || !strings.Contains(got, ",NULL,NULL,") {
		t.Fatalf("nullable dropoff: %s, %v", got, err)
	}
	for _, tc := range []struct {
		index int
		value string
	}{{3, ""}, {3, "not a timestamp"}, {12, "32768"}, {0, "1;DROP TABLE trips"}, {14, "NaN"}, {14, "Inf"}, {14, "1e999"}} {
		r := fixture()
		r[tc.index] = tc.value
		if _, err := rowSQL(r, 0); err == nil {
			t.Errorf("accepted column %d value %q", tc.index, tc.value)
		}
	}
	if _, err := rowSQL(r[:3], 0); err == nil {
		t.Fatal("accepted short row")
	}
	for _, shift := range []int64{math.MaxInt64, math.MinInt64} {
		if _, err := rowSQL(fixture(), shift); err == nil {
			t.Errorf("accepted shift %d", shift)
		}
	}
}

func TestMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"valid", metadata, true},
		{"CRLF", strings.ReplaceAll(metadata, "\n", "\r\n"), true},
		{"missing", strings.ReplaceAll(metadata, "SHIFT_SECONDS=86400\n", ""), false},
		{"zero rows", strings.ReplaceAll(metadata, "SOURCE_ROWS=2", "SOURCE_ROWS=0"), false},
		{"expression", strings.ReplaceAll(metadata, "SHIFT_SECONDS=86400", "SHIFT_SECONDS=$(date)"), false},
		{"duplicate", metadata + "SHIFT_SECONDS=1\n", false},
		{"invalid line", metadata + "broken\n", false},
		{"reversed", strings.ReplaceAll(metadata, "MIN_EPOCH=1704067200", "MIN_EPOCH=1704067300"), false},
		{"overflow", strings.ReplaceAll(metadata, "SHIFT_SECONDS=86400", "SHIFT_SECONDS=9223372036854775807"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "seed-window.env")
			if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readMetadata(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func seedFile(t *testing.T, rows int, header string) string {
	t.Helper()
	dir := t.TempDir()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = fmt.Fprintln(w, header)
	for range rows {
		_, _ = fmt.Fprintln(w, strings.Join(fixture(), "\t"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trips.gz"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadFile(t *testing.T) {
	for _, size := range []int{1, 1000, 1001} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			requests, inserted := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				u, p, ok := r.BasicAuth()
				if !ok || u != "seeder" || p != "fixture-password" || r.Form.Get("db") != "public" || r.URL.Path != "/v1/sql" {
					t.Errorf("incorrect SQL request: %s", r.URL)
				}
				query := r.Form.Get("sql")
				n := strings.Count(query, "),(") + 1
				inserted += n
				_, _ = fmt.Fprintf(w, `{"output":[{"affectedrows":%d}]}`, n)
			}))
			defer srv.Close()
			s := &seeder{
				baseURL: srv.URL, database: "public", user: "seeder", password: "fixture-password", client: srv.Client(),
				dataDir: seedFile(t, size, strings.Join(sourceNames(), "\t")), log: io.Discard,
			}
			rows, err := s.loadFile("trips.gz", 86400)
			if err != nil || rows != int64(size) || inserted != size || requests != (size+999)/1000 {
				t.Fatalf("rows=%d inserted=%d requests=%d err=%v", rows, inserted, requests, err)
			}
		})
	}
}

func TestSQLFailuresNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{500, `{"error":"unavailable"}`},
		{200, `{"code":1000,"error":"failed"}`},
		{200, `{"output":[]}`},
		{200, `not json`},
		{200, `{"output":[{"affectedrows":0}]}`},
		{200, `{"output":[{}]}`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			s := &seeder{baseURL: srv.URL, client: srv.Client(), dataDir: seedFile(t, 1, strings.Join(sourceNames(), "\t"))}
			if _, err := s.loadFile("trips.gz", 0); err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestBadSeedFile(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		dir := seedFile(t, 1, "wrong header")
		if corrupt {
			dir = seedFile(t, 1, strings.Join(sourceNames(), "\t"))
			path := filepath.Join(dir, "trips.gz")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			b[len(b)-8] ^= 1 // corrupt the gzip checksum
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		s := &seeder{dataDir: dir}
		if _, err := s.loadFile("trips.gz", 0); err == nil {
			t.Fatalf("accepted corrupt=%v", corrupt)
		}
	}
}

const facts = `{"output":[{"records":{"rows":[[2,1704153600,1704153602,1704153660,1704153662,0,0,0,2]]}}]}`

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		valid          bool
	}{
		{"valid", facts, true},
		{"lost duplicate", strings.Replace(facts, "[[2,", "[[1,", 1), false},
		{"wrong bounds", strings.Replace(facts, "1704153600", "1704153601", 1), false},
		{"date mismatch", strings.Replace(facts, ",0,0,0,2", ",1,0,0,2", 1), false},
		{"incomplete rollup", strings.Replace(facts, ",0,0,0,2", ",0,0,0,1", 1), false},
		{"NULL bound", strings.Replace(facts, "1704153600", "null", 1), false},
		{"missing records", `{"output":[{}]}`, false},
		{"short row", `{"output":[{"records":{"rows":[[2]]}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.response)
			}))
			defer srv.Close()
			s := &seeder{baseURL: srv.URL, client: srv.Client(), log: io.Discard}
			path := filepath.Join(t.TempDir(), "metadata")
			if err := os.WriteFile(path, []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			m, err := readMetadata(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.validate(m); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
			m["SEED_EPOCH"]++
			if err := s.validate(m); err == nil {
				t.Fatal("accepted uncentered window")
			}
		})
	}
}

func TestRunRecreatesTables(t *testing.T) {
	dir := seedFile(t, 1, strings.Join(sourceNames(), "\t"))
	b, err := os.ReadFile(filepath.Join(dir, "trips.gz"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"trips_1.gz", "trips_2.gz"} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "seed-window.env"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		q := r.Form.Get("sql")
		queries = append(queries, q)
		switch {
		case strings.HasPrefix(q, "SELECT count(*)"):
			_, _ = io.WriteString(w, facts)
		case strings.HasPrefix(q, "INSERT INTO trips ("):
			_, _ = io.WriteString(w, `{"output":[{"affectedrows":1}]}`)
		default:
			_, _ = io.WriteString(w, `{"output":[{"affectedrows":0}]}`)
		}
	}))
	defer srv.Close()
	s := &seeder{baseURL: srv.URL, client: srv.Client(), log: io.Discard, dataDir: dir}
	for range 2 {
		queries = nil
		if err := s.run(); err != nil {
			t.Fatal(err)
		}
		if len(queries) != 8 || queries[0] != "DROP TABLE IF EXISTS trips_15m" || queries[1] != "DROP TABLE IF EXISTS trips" {
			t.Fatalf("unexpected seed lifecycle: %v", queries)
		}
		for _, clause := range []string{`"pickup_datetime" TIMESTAMP(6) NOT NULL TIME INDEX`, "PRIMARY KEY (cab_type, vendor_id)", "append_mode='true'"} {
			if !strings.Contains(queries[2], clause) {
				t.Errorf("schema missing %s", clause)
			}
		}
	}
}
