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
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fixtureHeader = "trip_id\tpickup_datetime\tdropoff_datetime\tpassenger_count\ttrip_distance\t" +
	"tip_amount\ttotal_amount\tpayment_type\tcab_type\tpickup_borough_name\n"

// fixture rows are at 2024-01-01T00:00:00Z (1704067200) plus the shown offsets;
// the seed shift below moves them forward by one day
var fixtureFile1 = []string{
	"1\t2024-01-01 00:00:10\t2024-01-01 00:05:00\t1\t0.4\t1\t10.5\tCSH\torange\tThistlemoor",
	"2\t2024-01-01 00:00:50\t2024-01-01 00:02:00\t2\t3\t0\t20\tCRE\tblue\t",
}

var fixtureFile2 = []string{
	"3\t2024-01-01 00:01:30\t2024-01-01 00:10:00\t3\t25.25\t2.25\t40.05\tCSH\torange\tThistlemoor",
}

const (
	fixtureShift = 86400
	fixtureStart = 1704067200 + fixtureShift
)

func writeGzip(t *testing.T, path string, rows []string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(fixtureHeader + strings.Join(rows, "\n") + "\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeGzip(t, filepath.Join(dir, "trips_1.gz"), fixtureFile1)
	writeGzip(t, filepath.Join(dir, "trips_2.gz"), fixtureFile2)
	env := "SOURCE_ROWS=3\nSOURCE_PICKUP_MIN_EPOCH=1704067210\nSOURCE_PICKUP_MAX_EPOCH=1704067290\n" +
		"SOURCE_DROPOFF_MIN_EPOCH=1704067320\nSOURCE_DROPOFF_MAX_EPOCH=1704067800\nSEED_EPOCH=1704153650\n" +
		"SHIFT_SECONDS=" + strconv.Itoa(fixtureShift) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "seed-window.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newFixtureAccumulator(t *testing.T) *accumulator {
	t.Helper()
	src, err := openTrips(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	return newAccumulator(src)
}

func text(t *testing.T, a *accumulator, at int64) string {
	t.Helper()
	if err := a.advance(at); err != nil {
		t.Fatal(err)
	}
	return string(a.appendText(nil))
}

func requireLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l+"\n") {
			t.Errorf("missing line %q in:\n%s", l, out)
		}
	}
}

func TestAccumulator(t *testing.T) {
	a := newFixtureAccumulator(t)
	if out := text(t, a, fixtureStart); out != "" {
		t.Errorf("expected no metrics before the first pickup, got:\n%s", out)
	}
	out := text(t, a, fixtureStart+60)
	requireLines(t, out,
		"# TYPE trips_total counter",
		`trips_total{borough="Thistlemoor",cab_type="orange",payment_type="CSH"} 1`,
		`trips_total{borough="unknown",cab_type="blue",payment_type="CRE"} 1`,
		`trips_fares_dollars_total{borough="Thistlemoor",cab_type="orange"} 10.5`,
		`trips_tips_dollars_total{borough="Thistlemoor"} 1`,
		`trips_passengers_total{borough="unknown"} 2`,
		`trips_in_progress{borough="Thistlemoor"} 1`,
		`trips_distance_miles_bucket{borough="Thistlemoor",le="0.5"} 1`,
		`trips_distance_miles_count{borough="unknown"} 1`,
		`trips_distance_miles_sum{borough="unknown"} 3`,
	)
	out = text(t, a, fixtureStart+600)
	requireLines(t, out,
		`trips_total{borough="Thistlemoor",cab_type="orange",payment_type="CSH"} 2`,
		`trips_fares_dollars_total{borough="Thistlemoor",cab_type="orange"} 50.55`,
		`trips_tips_dollars_total{borough="Thistlemoor"} 3.25`,
		`trips_in_progress{borough="Thistlemoor"} 0`,
		`trips_in_progress{borough="unknown"} 0`,
		`trips_distance_miles_bucket{borough="Thistlemoor",le="20"} 1`,
		`trips_distance_miles_bucket{borough="Thistlemoor",le="+Inf"} 2`,
		`trips_distance_miles_sum{borough="Thistlemoor"} 25.65`,
	)
}

func TestBackfill(t *testing.T) {
	dir := writeFixture(t)
	out := filepath.Join(t.TempDir(), "om", "trips.om")
	var log bytes.Buffer
	err := backfill(backfillOptions{
		dataDir: dir, out: out, step: time.Minute, window: 2 * time.Minute,
		now: time.Unix(fixtureStart+150, 0),
	}, &log)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	om := string(b)
	// the window starts at 00:00:30 and rounds up to 00:01:00, yet counts the trips before it
	first, second := strconv.Itoa(fixtureStart+60), strconv.Itoa(fixtureStart+120)
	requireLines(t, om,
		"# TYPE trips counter",
		`trips_total{borough="Thistlemoor",cab_type="orange",payment_type="CSH"} 1 `+first,
		`trips_total{borough="Thistlemoor",cab_type="orange",payment_type="CSH"} 2 `+second,
		`trips_total{borough="unknown",cab_type="blue",payment_type="CRE"} 1 `+first,
		`trips_in_progress{borough="unknown"} 0 `+second,
		"# TYPE trips_distance_miles histogram",
		`trips_distance_miles_count{borough="Thistlemoor"} 2 `+second,
	)
	if !strings.HasSuffix(om, "\n# EOF\n") {
		t.Error("expected the output to end with # EOF")
	}
	if strings.Contains(om, " "+strconv.Itoa(fixtureStart)+"\n") {
		t.Error("expected no samples before the window")
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected the temporary file to be removed")
	}
	if !strings.Contains(log.String(), "backfilled") {
		t.Errorf("unexpected log %q", log.String())
	}
}

func TestBackfillPointsAreContiguousAndOrdered(t *testing.T) {
	dir := writeFixture(t)
	out := filepath.Join(t.TempDir(), "trips.om")
	err := backfill(backfillOptions{
		dataDir: dir, out: out, step: time.Minute, window: time.Hour,
		now: time.Unix(fixtureStart+3600, 0),
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	last, ended := map[string]int64{}, map[string]bool{}
	prev := ""
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		series := line[:strings.LastIndexByte(line[:i], ' ')]
		ts, _ := strconv.ParseInt(line[i+1:], 10, 64)
		// a histogram point spans several series, so only non-histogram runs must stay unbroken
		if !strings.HasPrefix(series, "trips_distance_miles") && series != prev {
			if ended[series] {
				t.Fatalf("points of %s are not contiguous", series)
			}
			ended[prev] = true
		}
		if ts <= last[series] {
			t.Fatalf("points of %s are out of order", series)
		}
		last[series], prev = ts, series
	}
}

func TestBackfillErrors(t *testing.T) {
	dir := writeFixture(t)
	for name, o := range map[string]backfillOptions{
		"fractional step": {dataDir: dir, step: 1500 * time.Millisecond, window: time.Hour},
		"short window":    {dataDir: dir, step: time.Minute, window: time.Second},
		"missing data":    {dataDir: t.TempDir(), step: time.Minute, window: time.Hour},
		"before any trip": {dataDir: dir, out: filepath.Join(t.TempDir(), "x"), step: time.Minute, window: time.Hour, now: time.Unix(fixtureStart, 0)},
	} {
		if err := backfill(o, io.Discard); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestExporterMatchesBackfill(t *testing.T) {
	dir := writeFixture(t)
	at := int64(fixtureStart + 120)
	out := filepath.Join(t.TempDir(), "trips.om")
	if err := backfill(backfillOptions{dataDir: dir, out: out, step: time.Minute, window: time.Hour,
		now: time.Unix(at, 0)}, io.Discard); err != nil {
		t.Fatal(err)
	}
	om, _ := os.ReadFile(out)

	src, err := openTrips(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	e := &exporter{acc: newAccumulator(src), now: func() time.Time { return time.Unix(at, 0) }}
	w := httptest.NewRecorder()
	newMux(e).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/metrics", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != textFormat {
		t.Fatalf("unexpected response %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	// every scraped sample must equal the last backfilled sample of the same series
	for line := range strings.SplitSeq(strings.TrimSpace(w.Body.String()), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(string(om), line+" "+strconv.FormatInt(at, 10)+"\n") {
			t.Errorf("scraped %q has no matching backfilled sample", line)
		}
	}
}

func TestMockRoutes(t *testing.T) {
	src, err := openTrips(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mux := newMux(&exporter{acc: newAccumulator(src), now: time.Now})
	for _, path := range []string{"/byterange/x", "/prometheus/api/v1/query?query=up"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected 200 got %d", path, w.Code)
		}
	}
}

func TestOpenTripsErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := openTrips(dir); err == nil {
		t.Error("expected an error without seed-window.env")
	}
	for _, env := range []string{"SHIFT_SECONDS=1\n", "SHIFT_SECONDS=x\n"} {
		if err := os.WriteFile(filepath.Join(dir, "seed-window.env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openTrips(dir); err == nil {
			t.Errorf("%q: expected an error", env)
		}
	}
	dir = writeFixture(t)
	if err := os.Remove(filepath.Join(dir, "trips_2.gz")); err != nil {
		t.Fatal(err)
	}
	if _, err := openTrips(dir); err == nil {
		t.Error("expected an error for a missing seed file")
	}
}

func TestBadRows(t *testing.T) {
	for name, row := range map[string]string{
		"short row":    "1\t2024-01-01 00:00:10",
		"bad pickup":   "1\tnope\t2024-01-01 00:05:00\t1\t0.4\t1\t10.5\tCSH\torange\tThistlemoor",
		"bad decimals": "1\t2024-01-01 00:00:10\t2024-01-01 00:05:00\t1\t0.425\t1\t10.5\tCSH\torange\tThistlemoor",
	} {
		dir := writeFixture(t)
		writeGzip(t, filepath.Join(dir, "trips_1.gz"), []string{row})
		src, err := openTrips(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := newAccumulator(src).advance(fixtureStart + 3600); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		src.Close()
	}
	dir := writeFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "trips_1.gz"), []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, _ := openTrips(dir)
	if err := newAccumulator(src).advance(fixtureStart + 3600); err == nil {
		t.Error("expected an error for a corrupt file")
	}
}

func TestParseHundredths(t *testing.T) {
	for in, expected := range map[string]int64{"0": 0, "3": 300, "8.5": 850, "11.58": 1158, "-0.5": -50, "-2.25": -225} {
		if v, err := parseHundredths([]byte(in)); err != nil || v != expected {
			t.Errorf("%s: expected %d got %d (%v)", in, expected, v, err)
		}
	}
	for _, in := range []string{"", "x", "1.x", "1.234", "1.-5"} {
		if _, err := parseHundredths([]byte(in)); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
	if s := string(appendValue(nil, -1205, true)); s != "-12.05" {
		t.Errorf("expected -12.05 got %s", s)
	}
}

func TestRun(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"backfill", "-bogus"}, {"serve", "-bogus"}} {
		if err := run(args, io.Discard); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
	t.Setenv("DEVORIGIN_STEP", "2m")
	if d := envDuration("DEVORIGIN_STEP", time.Minute); d != 2*time.Minute {
		t.Errorf("expected 2m got %s", d)
	}
	dir := writeFixture(t)
	out := filepath.Join(t.TempDir(), "trips.om")
	if err := run([]string{"backfill", "-seed-data", dir, "-out", out}, io.Discard); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
	if err := run([]string{"serve", "-seed-data", t.TempDir()}, io.Discard); err == nil {
		t.Error("expected an error without seed data")
	}
}

func TestOverflowGuards(t *testing.T) {
	if v, err := parseHundredths([]byte("92233720368547757.99")); err != nil || v != 9223372036854775799 {
		t.Errorf("expected the largest in-range value, got %d (%v)", v, err)
	}
	for _, in := range []string{"92233720368547758", "-92233720368547758", "99999999999999999999"} {
		if _, err := parseHundredths([]byte(in)); err == nil {
			t.Errorf("%s: expected an out-of-range error", in)
		}
	}
	if s := string(appendValue(nil, math.MinInt64, true)); s != "-92233720368547758.08" {
		t.Errorf("expected -92233720368547758.08 got %s", s)
	}
	if s := string(appendValue(nil, math.MaxInt64, true)); s != "92233720368547758.07" {
		t.Errorf("expected 92233720368547758.07 got %s", s)
	}

	dir := writeFixture(t)
	err := backfill(backfillOptions{dataDir: dir, out: filepath.Join(t.TempDir(), "x"), step: time.Second,
		window: 30 * 24 * time.Hour, now: time.Unix(fixtureStart+3600, 0)}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "samples per series") {
		t.Errorf("expected the step limit error, got %v", err)
	}

	env := "SOURCE_ROWS=3\nSOURCE_PICKUP_MIN_EPOCH=1\nSOURCE_PICKUP_MAX_EPOCH=2\nSEED_EPOCH=3\nSHIFT_SECONDS=1099511627777\n"
	if err := os.WriteFile(filepath.Join(dir, "seed-window.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openTrips(dir); err == nil {
		t.Error("expected an out-of-range shift error")
	}
}

func TestServerLimits(t *testing.T) {
	src, err := openTrips(writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	srv := newServer("127.0.0.1:0", &exporter{acc: newAccumulator(src), now: time.Now})
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 ||
		srv.MaxHeaderBytes <= 0 {
		t.Errorf("expected every server deadline and limit to be set: %+v", srv)
	}
	body := "query=up&pad=" + strings.Repeat("x", maxBodyBytes)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/prometheus/api/v1/query", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected an oversized body to be cut off, got %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/prometheus/api/v1/query", strings.NewReader("query=up"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("expected a small body to be served, got %d", w.Code)
	}
}
