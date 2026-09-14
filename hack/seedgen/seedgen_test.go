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
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const columnCount = 45

// memSink captures both uncompressed files for row-level inspection.
type memSink struct{ files [2]bytes.Buffer }

func (m *memSink) file(i int) (io.WriteCloser, error) { return nopCloser{&m.files[i]}, nil }

func TestGoldenSmall(t *testing.T) {
	sum, err := generate(config{rowsPerDay: profiles["small"]}, hashOnly{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.sha256 != golden["small"] {
		t.Fatalf("small profile sha256 = %s, want %s", sum.sha256, golden["small"])
	}
}

func TestGoldenDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("default profile takes a few seconds")
	}
	sum, err := generate(config{rowsPerDay: profiles["default"]}, hashOnly{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.sha256 != golden["default"] {
		t.Fatalf("default profile sha256 = %s, want %s", sum.sha256, golden["default"])
	}
}

func TestDeterministicAcrossRuns(t *testing.T) {
	var a, b memSink
	if _, err := generate(config{rowsPerDay: 240}, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := generate(config{rowsPerDay: 240}, &b); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if !bytes.Equal(a.files[i].Bytes(), b.files[i].Bytes()) {
			t.Fatalf("file %d differs between runs", i+1)
		}
	}
}

func TestRowsAreValid(t *testing.T) {
	var m memSink
	sum, err := generate(config{rowsPerDay: profiles["small"], stats: true}, &m)
	if err != nil {
		t.Fatal(err)
	}
	enums := map[int]map[string]bool{
		1:  {"1": true, "2": true},
		22: {"CSH": true, "CRE": true, "NOC": true, "DIS": true},
		26: {"orange": true, "blue": true, "purple": true},
	}
	var rows int64
	var prevID uint64
	var prevPickup string
	for i := range 2 {
		sc := bufio.NewScanner(&m.files[i])
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		if !sc.Scan() || sc.Text()+"\n" != header {
			t.Fatalf("file %d: bad header", i+1)
		}
		for sc.Scan() {
			rows++
			f := strings.Split(sc.Text(), "\t")
			if len(f) != columnCount {
				t.Fatalf("row %d has %d fields", rows, len(f))
			}
			for col, allowed := range enums {
				if !allowed[f[col]] {
					t.Fatalf("row %d: column %d value %q not allowed", rows, col, f[col])
				}
			}
			id, _ := strconv.ParseUint(f[0], 10, 64)
			if id <= prevID {
				t.Fatalf("row %d: trip_id %d not increasing", rows, id)
			}
			prevID = id
			if f[3] < prevPickup {
				t.Fatalf("row %d: pickup %s before previous %s", rows, f[3], prevPickup)
			}
			prevPickup = f[3]
			if f[5] < f[3] || f[2] != f[3][:10] || f[4] != f[5][:10] {
				t.Fatalf("row %d: inconsistent dates %q %q %q %q", rows, f[2], f[3], f[4], f[5])
			}
			if len(f[33]) > 4 || len(f[42]) > 4 || len(f[26]) > 6 || len(f[24]) > 25 || len(f[25]) > 25 {
				t.Fatalf("row %d: fixed-width column overflow", rows)
			}
			for _, col := range []int{27, 36} {
				gid, err := strconv.Atoi(f[col])
				if err != nil || gid < 0 || gid > 255 {
					t.Fatalf("row %d: zone gid %q out of uint8 range", rows, f[col])
				}
			}
			var parts float64
			for _, col := range []int{14, 15, 16, 17, 18, 19, 20} {
				v, err := strconv.ParseFloat(f[col], 64)
				if err != nil {
					t.Fatalf("row %d: bad money %q", rows, f[col])
				}
				parts += v
			}
			total, _ := strconv.ParseFloat(f[21], 64)
			if math.Abs(parts-total) > 0.005 {
				t.Fatalf("row %d: total %v != sum %v", rows, total, parts)
			}
			tip, _ := strconv.ParseFloat(f[17], 64)
			if tip > 0 && f[22] != "CSH" {
				t.Fatalf("row %d: tip on %s row", rows, f[22])
			}
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if rows != sum.rows || sum.rowsPerFile[0]+sum.rowsPerFile[1] != rows {
		t.Fatalf("row accounting: scanned %d, summary %d (%v)", rows, sum.rows, sum.rowsPerFile)
	}
	if sum.pickupMin < windowStart || sum.pickupMax >= windowStart+windowDays*secondsPerDay {
		t.Fatalf("pickup window %d..%d outside the synthetic window", sum.pickupMin, sum.pickupMax)
	}
}

func TestDistributions(t *testing.T) {
	sum, err := generate(config{rowsPerDay: profiles["small"], stats: true}, hashOnly{})
	if err != nil {
		t.Fatal(err)
	}
	n := float64(sum.rows)
	share := func(m map[string]int64, k string) float64 { return 100 * float64(m[k]) / n }
	near := func(name string, got, want, tol float64) {
		t.Helper()
		if math.Abs(got-want) > tol {
			t.Errorf("%s = %.2f, want %.2f ±%.2f", name, got, want, tol)
		}
	}
	near("orange share", share(sum.rowsByCabType, "orange"), 69, 2)
	near("blue share", share(sum.rowsByCabType, "blue"), 22, 2)
	near("purple share", share(sum.rowsByCabType, "purple"), 9, 2)
	near("top neighborhood share", share(sum.pickupsByNTA, namedNeighborhoods[0].name), 17.2, 1.5)
	near("airport share", share(sum.pickupsByNTA, "Skyport"), 4.7, 1)
	near("unknown share", share(sum.pickupsByNTA, ""), 1.5, 0.5)
	if len(sum.pickupsByNTA) < 180 {
		t.Errorf("only %d distinct pickup neighborhoods", len(sum.pickupsByNTA))
	}
	// weekday hour shares within a point of the target curve
	var weekdayRows float64
	for d := range windowDays {
		if !isWeekend(d) {
			weekdayRows += float64(sum.rowsByDay[d])
		}
	}
	for h := range 24 {
		got := 100 * float64(sum.rowsByHour[h]) / n
		near("hour "+strconv.Itoa(h), got, (hourShareWeekday[h]*5+hourShareWeekend[h]*2)/7, 0.8)
	}
	// day-of-week factors relative to Thursday
	var byDow [7]float64
	for d := range windowDays {
		byDow[d%7] += float64(sum.rowsByDay[d])
	}
	for i := range 7 {
		near("dow "+strconv.Itoa(i), byDow[i]/byDow[3], dowFactor[i], 0.04)
	}
}

func TestRunWritesAndSkips(t *testing.T) {
	dir := t.TempDir()
	var log bytes.Buffer
	args := []string{"-out", dir, "-profile", "small", "-seed-epoch", "1789000000"}
	if err := run(args, &log); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fileName1, fileName2, sidecarName, envName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	env, _ := os.ReadFile(filepath.Join(dir, envName))
	for _, key := range []string{"SOURCE_ROWS=", "SOURCE_PICKUP_MIN_EPOCH=", "SOURCE_PICKUP_MAX_EPOCH=",
		"SOURCE_DROPOFF_MIN_EPOCH=", "SOURCE_DROPOFF_MAX_EPOCH=", "SEED_EPOCH=1789000000", "SHIFT_SECONDS="} {
		if !strings.Contains(string(env), key) {
			t.Fatalf("seed-window.env missing %s:\n%s", key, env)
		}
	}
	first, _ := os.Stat(filepath.Join(dir, fileName1))
	log.Reset()
	if err := run(args, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "skipping") {
		t.Fatalf("second run did not skip: %s", log.String())
	}
	second, _ := os.Stat(filepath.Join(dir, fileName1))
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatal("second run rewrote the data file")
	}
	// the gzip stream must decode to the header
	f, _ := os.Open(filepath.Join(dir, fileName1))
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if !gz.ModTime.IsZero() || gz.Name != "" {
		t.Fatalf("gzip header not fixed: %v %q", gz.ModTime, gz.Name)
	}
	buf := make([]byte, len(header))
	if _, err := io.ReadFull(gz, buf); err != nil || string(buf) != header {
		t.Fatalf("decoded header mismatch: %v", err)
	}
	if err := run([]string{"-out", dir, "-profile", "nope"}, &log); err == nil {
		t.Fatal("expected unknown profile error")
	}
	if err := run([]string{"-verify-only", "-profile", "small"}, &log); err != nil {
		t.Fatal(err)
	}
}

func TestTablePick(t *testing.T) {
	tb := newTable([]float64{1, 1, 2})
	for _, c := range []struct {
		u    float64
		want int
	}{{0, 0}, {0.24, 0}, {0.25, 1}, {0.49, 1}, {0.5, 2}, {0.999, 2}} {
		if got := tb.pick(c.u); got != c.want {
			t.Errorf("pick(%v) = %d, want %d", c.u, got, c.want)
		}
	}
}
