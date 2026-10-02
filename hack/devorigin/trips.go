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
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var seedFiles = []string{"trips_1.gz", "trips_2.gz"}

var requiredKeys = []string{
	"SOURCE_ROWS", "SOURCE_PICKUP_MIN_EPOCH", "SOURCE_PICKUP_MAX_EPOCH",
	"SEED_EPOCH", "SHIFT_SECONDS",
}

const (
	unknownBorough = "unknown"
	// about 35,000 years; larger shifts could overflow the shifted timestamps
	maxShiftSeconds = 1 << 40
)

const (
	colPickup = iota // columns are located by name in each file's header
	colDropoff
	colPassengers
	colDistance
	colTip
	colTotal
	colPayment
	colCab
	colBorough
	numCols
)

var columnNames = [numCols]string{
	"pickup_datetime", "dropoff_datetime", "passenger_count", "trip_distance",
	"tip_amount", "total_amount", "payment_type", "cab_type", "pickup_borough_name",
}

type trip struct {
	pickup, dropoff int64 // epoch seconds, shifted
	passengers      int64
	distance        int64 // hundredths of a mile
	tip, total      int64 // cents
	borough         string
	cab             string
	payment         string
}

func readShift(dir string) (int64, error) {
	path := filepath.Join(dir, "seed-window.env")
	raw, err := os.ReadFile(path) //nolint:gosec // the seed data directory is operator-supplied
	if err != nil {
		return 0, fmt.Errorf("missing %s; run seed_data_generate first", path)
	}
	values := map[string]int64{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid %s in %s", key, path)
		}
		values[key] = n
	}
	var missing []string
	for _, key := range requiredKeys {
		if _, ok := values[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return 0, fmt.Errorf("missing metadata fields in %s: %s", path, strings.Join(missing, ", "))
	}
	shift := values["SHIFT_SECONDS"]
	if shift < -maxShiftSeconds || shift > maxShiftSeconds {
		return 0, fmt.Errorf("SHIFT_SECONDS %d in %s is out of range", shift, path)
	}
	return shift, nil
}

type tripSource struct {
	files   []string
	shift   int64
	file    *os.File
	gz      *gzip.Reader
	scanner *bufio.Scanner
	cols    [numCols]int
	maxCol  int
	fields  [][]byte
	next    trip
	hasNext bool
}

func openTrips(dir string) (*tripSource, error) {
	shift, err := readShift(dir)
	if err != nil {
		return nil, err
	}
	s := &tripSource{shift: shift}
	for _, name := range seedFiles {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err != nil || st.Size() == 0 { //nolint:gosec // operator-supplied
			return nil, fmt.Errorf("missing or empty seed file %s", path)
		}
		s.files = append(s.files, path)
	}
	return s, nil
}

func (s *tripSource) Close() error {
	var err error
	if s.gz != nil {
		err = s.gz.Close()
		s.gz = nil
	}
	if s.file != nil {
		err = errors.Join(err, s.file.Close())
		s.file = nil
	}
	s.scanner = nil
	return err
}

func (s *tripSource) peek() (trip, bool, error) {
	// the seed files are streamed in order, and their rows are sorted by pickup time
	if s.hasNext {
		return s.next, true, nil
	}
	for {
		if s.scanner == nil {
			if len(s.files) == 0 {
				return trip{}, false, nil
			}
			if err := s.openNext(); err != nil {
				return trip{}, false, err
			}
		}
		if s.scanner.Scan() {
			t, err := s.parse(s.scanner.Bytes())
			if err != nil {
				return trip{}, false, err
			}
			s.next, s.hasNext = t, true
			return t, true, nil
		}
		if err := s.scanner.Err(); err != nil {
			return trip{}, false, err
		}
		if err := s.Close(); err != nil {
			return trip{}, false, err
		}
	}
}

func (s *tripSource) consume() {
	s.hasNext = false
}

func (s *tripSource) openNext() error {
	path := s.files[0]
	s.files = s.files[1:]
	f, err := os.Open(path) //nolint:gosec // the seed data directory is operator-supplied
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	s.file, s.gz = f, gz
	s.scanner = bufio.NewScanner(gz)
	s.scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if !s.scanner.Scan() {
		return fmt.Errorf("%s: missing header row: %w", path, errors.Join(s.scanner.Err(), io.ErrUnexpectedEOF))
	}
	return s.readHeader(path, s.scanner.Bytes())
}

func (s *tripSource) readHeader(path string, line []byte) error {
	names := bytes.Split(line, []byte{'\t'})
	s.maxCol = 0
	for i, want := range columnNames {
		s.cols[i] = -1
		for j, name := range names {
			if string(name) == want {
				s.cols[i] = j
				s.maxCol = max(s.maxCol, j)
				break
			}
		}
		if s.cols[i] < 0 {
			return fmt.Errorf("%s: header has no %s column", path, want)
		}
	}
	return nil
}

func (s *tripSource) parse(line []byte) (trip, error) {
	s.fields = s.fields[:0]
	for len(s.fields) <= s.maxCol {
		field, rest, found := bytes.Cut(line, []byte{'\t'})
		s.fields = append(s.fields, field)
		if !found {
			break
		}
		line = rest
	}
	if len(s.fields) <= s.maxCol {
		return trip{}, fmt.Errorf("short row: %d fields", len(s.fields))
	}
	f := func(col int) []byte { return s.fields[s.cols[col]] }
	var t trip
	var err error
	if t.pickup, err = parseDateTime(f(colPickup)); err != nil {
		return trip{}, err
	}
	if t.dropoff, err = parseDateTime(f(colDropoff)); err != nil {
		return trip{}, err
	}
	t.pickup += s.shift
	t.dropoff += s.shift
	if t.passengers, err = strconv.ParseInt(string(f(colPassengers)), 10, 64); err != nil {
		return trip{}, fmt.Errorf("passenger_count: %w", err)
	}
	if t.distance, err = parseHundredths(f(colDistance)); err != nil {
		return trip{}, fmt.Errorf("trip_distance: %w", err)
	}
	if t.tip, err = parseHundredths(f(colTip)); err != nil {
		return trip{}, fmt.Errorf("tip_amount: %w", err)
	}
	if t.total, err = parseHundredths(f(colTotal)); err != nil {
		return trip{}, fmt.Errorf("total_amount: %w", err)
	}
	t.borough = intern(f(colBorough))
	if t.borough == "" {
		t.borough = unknownBorough
	}
	t.cab, t.payment = intern(f(colCab)), intern(f(colPayment))
	return t, nil
}

var interned = map[string]string{}

func intern(b []byte) string {
	if s, ok := interned[string(b)]; ok {
		return s
	}
	s := string(b)
	interned[s] = s
	return s
}

func parseDateTime(b []byte) (int64, error) {
	t, err := time.Parse(time.DateTime, string(b))
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

func parseHundredths(b []byte) (int64, error) {
	whole, frac, _ := bytes.Cut(b, []byte{'.'})
	if len(frac) > 2 {
		return 0, fmt.Errorf("more than two decimals in %q", b)
	}
	neg := len(whole) > 0 && whole[0] == '-'
	w, err := strconv.ParseInt(string(whole), 10, 64)
	if err != nil {
		return 0, err
	}
	if w > math.MaxInt64/100-1 || w < math.MinInt64/100+1 {
		return 0, fmt.Errorf("%q is out of range", b)
	}
	var fv int64
	if len(frac) > 0 {
		if fv, err = strconv.ParseInt(string(frac), 10, 64); err != nil || fv < 0 {
			return 0, fmt.Errorf("invalid decimals in %q", b)
		}
		if len(frac) == 1 {
			fv *= 10
		}
	}
	if neg {
		return w*100 - fv, nil
	}
	return w*100 + fv, nil
}
