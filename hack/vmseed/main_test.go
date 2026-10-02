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
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testOM = "# TYPE trips counter\n# HELP trips trips\ntrips_total{cab_type=\"blue\"} 3 1700000000\n" +
	"trips_total{cab_type=\"blue\"} 5 1700000060\n# TYPE trips_in_progress gauge\n" +
	"trips_in_progress 2 1699999940\n# EOF\n"

// fakeVM records what the seeder sends and answers count queries from it.
type fakeVM struct {
	mu           sync.Mutex
	history      bool
	liveFrom     int64 // first live trips sample, or zero
	lastFixture  int64 // newest canary samples, or zero
	lastGraphite int64
	unhealthy    int
	importCode   int
	tripLabels   string
	trips        int64
	fixtures     int64
	graphite     int64
	resets       int
	queryShort   int64 // subtracted from counts, to exercise validation failures
	findReply    string
	tagsReply    string
	badQueryVal  bool
}

func (f *fakeVM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/health":
		if f.unhealthy > 0 {
			f.unhealthy--
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "OK")
	case "/api/v1/import/prometheus":
		if f.importCode != 0 {
			http.Error(w, "rejected", f.importCode)
			return
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil || r.Header.Get("Content-Encoding") != "gzip" {
			http.Error(w, "want gzip", http.StatusBadRequest)
			return
		}
		f.tripLabels = strings.Join(r.URL.Query()["extra_label"], ",")
		sc := bufio.NewScanner(zr)
		for sc.Scan() {
			l := sc.Text()
			if l == "" || l[0] == '#' {
				continue
			}
			ts, _ := strconv.ParseInt(l[strings.LastIndexByte(l, ' ')+1:], 10, 64)
			if f.liveFrom == 0 || ts < f.liveFrom {
				f.trips++
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case "/api/v1/import":
		dec := json.NewDecoder(r.Body)
		for {
			var line struct {
				Metric map[string]string `json:"metric"`
				Values []any             `json:"values"`
			}
			if err := dec.Decode(&line); err != nil {
				break
			}
			if strings.HasPrefix(line.Metric["__name__"], graphiteRoot+".") {
				f.graphite += int64(len(line.Values))
			} else {
				f.fixtures += int64(len(line.Values))
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case "/internal/resetRollupResultCache":
		f.resets++
	case "/api/v1/query":
		f.query(w, r)
	case "/metrics/find":
		_, _ = io.WriteString(w, f.findReply)
	case "/tags/autoComplete/tags":
		_, _ = io.WriteString(w, f.tagsReply)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeVM) query(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("nocache") != "1" || q.Get("latency_offset") != latencyOffset {
		http.Error(w, "missing cache bypass", http.StatusBadRequest)
		return
	}
	expr := q.Get("query")
	var n int64
	switch {
	case strings.HasPrefix(expr, "count("):
		n = map[bool]int64{true: 56}[f.history]
	case strings.HasPrefix(expr, "min(tfirst_over_time("):
		n = f.liveFrom
	case strings.Contains(expr, fixtureCanary):
		n = f.lastFixture
	case strings.Contains(expr, graphiteCanary):
		n = f.lastGraphite
	case strings.Contains(expr, tripsMatch):
		n = f.trips
	case strings.Contains(expr, fixtureMatch):
		n = f.fixtures
	case strings.Contains(expr, graphiteMatch):
		n = f.graphite
	default:
		_, _ = io.WriteString(w, `{"status":"error","error":"unknown query"}`)
		return
	}
	if n == 0 && !strings.HasPrefix(expr, "sum(") {
		_, _ = io.WriteString(w, `{"status":"success","data":{"result":[]}}`)
		return
	}
	v := fmt.Sprint(n - f.queryShort)
	if f.badQueryVal {
		v = "x"
	}
	_, _ = fmt.Fprintf(w, `{"status":"success","data":{"result":[{"metric":{},"value":[1,%q]}]}}`, v)
}

func newFakeVM() *fakeVM {
	return &fakeVM{
		findReply: `[{"text":"fast"},{"text":"gappy"},{"text":"slow"},{"text":"tagged"}]`,
		tagsReply: `["dc","name","tier"]`,
	}
}

func newTestSeeder(t *testing.T, url string) (*seeder, *bytes.Buffer) {
	t.Helper()
	om := filepath.Join(t.TempDir(), "trips.om")
	if err := os.WriteFile(om, []byte(testOM), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	return &seeder{
		baseURL:  url,
		omPath:   om,
		instance: "devorigin:8482",
		timeout:  10 * time.Second, // only reached when a test fails
		poll:     time.Millisecond,
		now:      func() time.Time { return time.Unix(1_700_000_030, 0) },
		client:   &http.Client{},
		log:      &log,
	}, &log
}

func TestRunImportsAndValidates(t *testing.T) {
	f := newFakeVM()
	f.unhealthy = 2
	ts := httptest.NewServer(f)
	defer ts.Close()
	s, log := newTestSeeder(t, ts.URL)
	if err := s.run(); err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	if f.trips != 3 || f.tripLabels != "job=trips,instance=devorigin:8482" {
		t.Errorf("trips import: %d samples, labels %q", f.trips, f.tripLabels)
	}
	prom, graphite := buildFixtures(s.now().Truncate(time.Minute).Unix())
	if f.fixtures != statsOf(prom).samples || f.graphite != statsOf(graphite).samples {
		t.Errorf("fixtures imported %d/%d", f.fixtures, f.graphite)
	}
	if f.resets != 1 {
		t.Errorf("rollup cache resets = %d", f.resets)
	}
	if _, err := os.Stat(s.omPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backfill file was not removed: %v", err)
	}
	for _, w := range []string{"imported 3 trips samples from 2023-11-14T22:12:20Z to 2023-11-14T22:14:20Z", "seed complete"} {
		if !strings.Contains(log.String(), w) {
			t.Errorf("log missing %q:\n%s", w, log)
		}
	}
}

func TestRunKeepsHistoryAndAppendsFixtures(t *testing.T) {
	f := newFakeVM()
	f.history = true
	s, log := newTestSeeder(t, "")
	anchor := s.now().Truncate(time.Minute).Unix()
	f.lastFixture, f.lastGraphite = anchor-120, anchor
	ts := httptest.NewServer(f)
	defer ts.Close()
	s.baseURL = ts.URL
	if err := s.run(); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	prom, _ := buildFixtures(anchor)
	if want := statsOf(after(prom, anchor-120)).samples; f.trips != 0 || f.fixtures != want || f.graphite != 0 {
		t.Errorf("imported trips=%d fixtures=%d (want %d) graphite=%d", f.trips, f.fixtures, want, f.graphite)
	}
	for _, w := range []string{"keeping it", "graphite fixtures are current"} {
		if !strings.Contains(log.String(), w) {
			t.Errorf("log missing %q:\n%s", w, log)
		}
	}
	if _, err := os.Stat(s.omPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("the backfill file was not removed")
	}
}

func TestRunCountsHistoryBeforeLiveSamples(t *testing.T) {
	for _, tc := range []struct {
		liveFrom int64
		want     string
	}{{1700000060, "verified 2 trips samples"}, {1699999900, "count is not verified"}} {
		f := newFakeVM()
		f.liveFrom = tc.liveFrom
		ts := httptest.NewServer(f)
		s, log := newTestSeeder(t, ts.URL)
		anchor := s.now().Truncate(time.Minute).Unix()
		f.lastFixture, f.lastGraphite = anchor, anchor
		err := s.run()
		ts.Close()
		if err != nil || !strings.Contains(log.String(), tc.want) {
			t.Errorf("liveFrom %d: %v\n%s", tc.liveFrom, err, log)
		}
	}
}

func TestRunFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeVM, *seeder)
		want   string
	}{
		{"unhealthy", func(f *fakeVM, _ *seeder) { f.unhealthy = 1 << 30 }, "not healthy"},
		{"import rejected", func(f *fakeVM, _ *seeder) { f.importCode = http.StatusBadRequest }, "400 Bad Request: rejected"},
		{"missing backfill", func(_ *fakeVM, s *seeder) { _ = os.Remove(s.omPath) }, "missing trips backfill"},
		{"count mismatch", func(f *fakeVM, _ *seeder) { f.queryShort = 1 }, "found 2 samples, want 3"},
		{"history probe", func(f *fakeVM, _ *seeder) { f.badQueryVal, f.history = true, true }, `bad value "x"`},
		{"bad value", func(f *fakeVM, _ *seeder) { f.badQueryVal = true }, `bad value "x"`},
		{"find mismatch", func(f *fakeVM, _ *seeder) { f.findReply = `[{"text":"fast"}]` }, "graphite find"},
		{"tags missing", func(f *fakeVM, _ *seeder) { f.tagsReply = `["name"]` }, `missing "dc"`},
		{"bad json", func(f *fakeVM, _ *seeder) { f.tagsReply = `{` }, "GET /tags/autoComplete/tags"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeVM()
			ts := httptest.NewServer(f)
			defer ts.Close()
			s, _ := newTestSeeder(t, ts.URL)
			s.timeout = 500 * time.Millisecond
			// recent canaries keep the appended fixtures small
			anchor := s.now().Truncate(time.Minute).Unix()
			f.lastFixture, f.lastGraphite = anchor-60, anchor-60
			tc.mutate(f, s)
			if err := s.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestQueryErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","error":"bad expr"}`)
	}))
	defer ts.Close()
	s, _ := newTestSeeder(t, ts.URL)
	if _, _, err := s.query(t.Context(), "up", 1); err == nil || !strings.Contains(err.Error(), "bad expr") {
		t.Fatalf("got %v", err)
	}
	s.baseURL = "http://127.0.0.1:0"
	if err := s.post(t.Context(), "/x", nil, ""); err == nil {
		t.Fatal("expected a connection error")
	}
	if err := s.get(t.Context(), "\x7f", nil); err == nil {
		t.Fatal("expected a request error")
	}
}

func TestCompressOpenMetrics(t *testing.T) {
	var out bytes.Buffer
	var st importStats
	if err := compressOpenMetrics(strings.NewReader(testOM), &out, &st); err != nil {
		t.Fatal(err)
	}
	if st != (importStats{samples: 3, minTS: 1699999940, maxTS: 1700000060, counted: 3, lastCounted: 1700000060}) {
		t.Errorf("stats %+v", st)
	}
	zr, err := gzip.NewReader(&out)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != testOM {
		t.Errorf("round trip changed the content: %q", b)
	}
	for _, tc := range []struct{ in, want string }{
		{"a 1 2\n", "no # EOF"},
		{"a 1 2.5\n# EOF\n", "integer timestamp"},
		{"novalue\n# EOF\n", "malformed"},
		{strings.Repeat("x", 70<<10) + "\n", "exceeds 64 KiB"},
	} {
		var st importStats
		if err := compressOpenMetrics(strings.NewReader(tc.in), io.Discard, &st); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Errorf("%.20q: got %v, want %q", tc.in, err, tc.want)
		}
	}
	if err := compressOpenMetrics(strings.NewReader(testOM), failWriter{}, &st); err == nil {
		t.Error("expected a write error")
	}
}

var errWriteFailed = errors.New("write failed")

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

func TestEnv(t *testing.T) {
	t.Setenv("VMSEED_TEST", "")
	if envOr("VMSEED_TEST", "x") != "x" || envDuration("VMSEED_TEST", time.Second) != time.Second {
		t.Error("fallbacks not used")
	}
	t.Setenv("VMSEED_TEST", "3m")
	if envOr("VMSEED_TEST", "x") != "3m" || envDuration("VMSEED_TEST", time.Second) != 3*time.Minute {
		t.Error("environment not used")
	}
}
