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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testEnv = "SOURCE_ROWS=10\nSOURCE_PICKUP_MIN_EPOCH=1704067200\nSOURCE_PICKUP_MAX_EPOCH=1711324799\n" +
	"SOURCE_DROPOFF_MIN_EPOCH=1704067300\nSOURCE_DROPOFF_MAX_EPOCH=1711330000\nSEED_EPOCH=1789000000\nSHIFT_SECONDS=81304000\n"

func writeSeedDir(t *testing.T, env string) string {
	t.Helper()
	dir := t.TempDir()
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, "seed-window.env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range seedFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeDruid models the handful of endpoints the seeder touches.
type fakeDruid struct {
	statusCalls  atomic.Int32
	sqlCalls     atomic.Int32
	markedUnused atomic.Int32
	spec         atomic.Pointer[map[string]any]
	existing     bool
}

func (f *fakeDruid) handler(w http.ResponseWriter, r *http.Request) {
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/druid/coordinator/v1/metadata/datasources":
		if f.existing {
			write([]string{"trips"})
		} else {
			write([]string{})
		}
	case strings.HasSuffix(r.URL.Path, "/segments"):
		write([]string{"seg1", "seg2"})
	case strings.HasSuffix(r.URL.Path, "/markUnused"):
		f.markedUnused.Add(1)
		write(map[string]any{"numChangedSegments": 2})
	case r.URL.Path == "/druid/indexer/v1/task" && r.Method == http.MethodPost:
		var spec map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &spec)
		f.spec.Store(&spec)
		write(map[string]any{"task": "task-1"})
	case strings.HasSuffix(r.URL.Path, "/status"):
		state := "RUNNING"
		if f.statusCalls.Add(1) >= 2 {
			state = "SUCCESS"
		}
		write(map[string]any{"status": map[string]any{"status": state}})
	case r.URL.Path == "/druid/v2/sql":
		if f.sqlCalls.Add(1) == 1 {
			http.Error(w, `{"error":"Object 'trips' not found"}`, http.StatusBadRequest)
			return
		}
		write([]map[string]any{{"rows": 10, "min_time": time.Unix(1704067200+81304000, 0).UTC().Format("2006-01-02T15:04:05.000Z"), "max_time": float64((1711324799 + 81304000) * 1000)}})
	default:
		http.NotFound(w, r)
	}
}

func newTestSeeder(t *testing.T, f *fakeDruid, dir string) *seeder {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	var log bytes.Buffer
	return &seeder{
		druidURL: srv.URL, datasource: "trips", dataDir: dir, timeout: 10 * time.Second,
		poll: time.Millisecond, retryBase: time.Millisecond, client: srv.Client(), log: &log,
	}
}

func TestRunEndToEnd(t *testing.T) {
	f := &fakeDruid{existing: true}
	s := newTestSeeder(t, f, writeSeedDir(t, testEnv))
	if err := s.run(); err != nil {
		t.Fatal(err)
	}
	if f.markedUnused.Load() != 1 {
		t.Fatal("existing segments were not marked unused")
	}
	spec := *f.spec.Load()
	text, _ := json.Marshal(spec)
	for _, want := range []string{`"__time + 81304000000"`, `"skipHeaderRows":1`, `"dataSource":"trips"`,
		`"intervals":["` + isoDay(1704067200+81304000) + "/" + isoDay(1711324799+81304000+86400) + `"]`, `"pickup_neighborhood_name"`, `"transit_tax"`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("spec missing %s:\n%s", want, text)
		}
	}
	if !strings.Contains(s.log.(*bytes.Buffer).String(), "druid seed complete: 10 rows") {
		t.Fatalf("unexpected log: %s", s.log.(*bytes.Buffer).String())
	}
}

func TestRunSkipsMarkUnusedForNewDatasource(t *testing.T) {
	f := &fakeDruid{}
	s := newTestSeeder(t, f, writeSeedDir(t, testEnv))
	if err := s.run(); err != nil {
		t.Fatal(err)
	}
	if f.markedUnused.Load() != 0 {
		t.Fatal("markUnused called for a datasource that does not exist")
	}
}

func TestReadMetadataErrors(t *testing.T) {
	cases := map[string]string{
		"missing file":   "",
		"missing key":    "SOURCE_ROWS=1\n",
		"invalid value":  "SOURCE_ROWS=abc\nSOURCE_PICKUP_MIN_EPOCH=1\nSOURCE_PICKUP_MAX_EPOCH=2\nSEED_EPOCH=3\nSHIFT_SECONDS=4\n",
		"non-positive":   "SOURCE_ROWS=0\nSOURCE_PICKUP_MIN_EPOCH=1\nSOURCE_PICKUP_MAX_EPOCH=2\nSEED_EPOCH=3\nSHIFT_SECONDS=4\n",
		"negative shift": "SOURCE_ROWS=10\nSOURCE_PICKUP_MIN_EPOCH=1\nSOURCE_PICKUP_MAX_EPOCH=2\nSEED_EPOCH=3\nSHIFT_SECONDS=-4\n",
	}
	for name, env := range cases {
		s := &seeder{dataDir: writeSeedDir(t, env)}
		_, err := s.readMetadata()
		if name == "negative shift" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", name, err)
			}
			spec := s.ingestionSpec(map[string]int64{"SHIFT_SECONDS": -4, "SOURCE_PICKUP_MIN_EPOCH": 1, "SOURCE_PICKUP_MAX_EPOCH": 2})
			if text, _ := json.Marshal(spec); !strings.Contains(string(text), `"__time - 4000"`) {
				t.Fatalf("negative shift expression missing: %s", text)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	s := &seeder{dataDir: t.TempDir()}
	if _, err := s.readMetadata(); err == nil || !strings.Contains(err.Error(), "run seed_data_generate") {
		t.Fatalf("expected missing-file guidance, got %v", err)
	}
	dir := writeSeedDir(t, testEnv)
	_ = os.Remove(filepath.Join(dir, "trips_2.gz"))
	if _, err := (&seeder{dataDir: dir}).readMetadata(); err == nil || !strings.Contains(err.Error(), "trips_2.gz") {
		t.Fatalf("expected missing data file error, got %v", err)
	}
}

func TestRequestJSONRetriesAndFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flaky":
			if calls.Add(1) < 3 {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/empty":
			w.WriteHeader(http.StatusOK)
		case "/bad":
			http.Error(w, strings.Repeat("x", 500), http.StatusForbidden)
		}
	}))
	defer srv.Close()
	s := &seeder{druidURL: srv.URL, retryBase: time.Microsecond, client: srv.Client()}
	if v, err := s.requestJSON("/flaky", nil, nil); err != nil || v.(map[string]any)["ok"] != true {
		t.Fatalf("flaky: %v %v", v, err)
	}
	if v, err := s.requestJSON("/empty", nil, nil); err != nil || v != nil {
		t.Fatalf("empty: %v %v", v, err)
	}
	if _, err := s.requestJSON("/bad", nil, nil); err == nil || !strings.Contains(err.Error(), "HTTP 403") || len(err.Error()) > 500 {
		t.Fatalf("bad: %v", err)
	}
	if v, err := s.requestJSON("/bad", nil, map[int]bool{403: true}); err != nil || v != nil {
		t.Fatalf("tolerated: %v %v", v, err)
	}
	srv.Close()
	if _, err := s.requestJSON("/flaky", nil, nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("closed server: %v", err)
	}
}

func TestWaitForTaskFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/reports") {
			_, _ = w.Write([]byte(`{"ingestionStatsAndErrors":{"reason":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":{"status":"FAILED"}}`))
	}))
	defer srv.Close()
	var log bytes.Buffer
	s := &seeder{druidURL: srv.URL, retryBase: time.Microsecond, poll: time.Microsecond, timeout: time.Second, client: srv.Client(), log: &log}
	if err := s.waitForTask("t1"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected failure report, got %v", err)
	}
	s.timeout = 0
	if err := s.waitForTask("t1"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestValidateBoundsMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"rows":10,"min_time":"2020-01-01T00:00:00.000Z","max_time":"2020-01-02T00:00:00.000Z"}]`))
	}))
	defer srv.Close()
	var log bytes.Buffer
	s := &seeder{druidURL: srv.URL, datasource: "trips", retryBase: time.Microsecond, poll: time.Microsecond, client: srv.Client(), log: &log}
	meta := map[string]int64{"SOURCE_ROWS": 10, "SOURCE_PICKUP_MIN_EPOCH": 1, "SOURCE_PICKUP_MAX_EPOCH": 2, "SHIFT_SECONDS": 3}
	if err := s.validate(meta); err == nil || !strings.Contains(err.Error(), "bounds mismatch") {
		t.Fatalf("expected bounds mismatch, got %v", err)
	}
	meta["SOURCE_ROWS"] = 11
	if err := s.validate(meta); err == nil || !strings.Contains(err.Error(), "expected 11 rows") {
		t.Fatalf("expected row-count timeout, got %v", err)
	}
}

func TestParseSQLEpoch(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int64
	}{{"2024-01-01T00:00:00.000Z", 1704067200}, {"2024-01-01T00:00:00", 1704067200}, {float64(1704067200123), 1704067200}} {
		got, err := parseSQLEpoch(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseSQLEpoch(%v) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	if _, err := parseSQLEpoch(true); err == nil {
		t.Error("expected error for unsupported type")
	}
	if v, ok := toInt("12"); !ok || v != 12 {
		t.Error("toInt string")
	}
	if _, ok := toInt(nil); ok {
		t.Error("toInt nil")
	}
}

func TestNewSeederFromEnv(t *testing.T) {
	t.Setenv("DRUID_URL", "http://example:8888/")
	t.Setenv("DRUID_SEED_TIMEOUT", "12.5")
	s := newSeederFromEnv()
	if s.druidURL != "http://example:8888" || s.timeout != 12500*time.Millisecond || s.datasource != "trips" {
		t.Fatalf("unexpected seeder %+v", s)
	}
}
