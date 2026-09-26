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

// Command greptimeseed loads the shared synthetic trips through GreptimeDB's
// HTTP SQL endpoint. SQL preserves DATE, timestamp and narrow numeric types
// that the InfluxDB line protocol cannot write into the existing schema.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type column struct {
	name string
	kind string
}

// Source order matches hack/seedgen. pickup_epoch is appended during loading.
var columns = []column{
	{"trip_id", "BIGINT"},
	{"vendor_id", "STRING"},
	{"pickup_date", "DATE"},
	{"pickup_datetime", "TIMESTAMP(6)"},
	{"dropoff_date", "DATE"},
	{"dropoff_datetime", "TIMESTAMP(6)"},
	{"store_and_fwd_flag", "SMALLINT"},
	{"rate_code_id", "SMALLINT"},
	{"pickup_longitude", "DOUBLE"},
	{"pickup_latitude", "DOUBLE"},
	{"dropoff_longitude", "DOUBLE"},
	{"dropoff_latitude", "DOUBLE"},
	{"passenger_count", "SMALLINT"},
	{"trip_distance", "DOUBLE"},
	{"fare_amount", "FLOAT"},
	{"extra", "FLOAT"},
	{"transit_tax", "FLOAT"},
	{"tip_amount", "FLOAT"},
	{"tolls_amount", "FLOAT"},
	{"ehail_fee", "FLOAT"},
	{"improvement_surcharge", "FLOAT"},
	{"total_amount", "FLOAT"},
	{"payment_type", "STRING"},
	{"trip_type", "SMALLINT"},
	{"pickup", "STRING"},
	{"dropoff", "STRING"},
	{"cab_type", "STRING"},
	{"pickup_zone_gid", "INT"},
	{"pickup_tract_label", "FLOAT"},
	{"pickup_borough_code", "SMALLINT"},
	{"pickup_borough_name", "STRING"},
	{"pickup_tract_code", "STRING"},
	{"pickup_district_class", "STRING"},
	{"pickup_neighborhood_code", "STRING"},
	{"pickup_neighborhood_name", "STRING"},
	{"pickup_ward", "INT"},
	{"dropoff_zone_gid", "INT"},
	{"dropoff_tract_label", "FLOAT"},
	{"dropoff_borough_code", "SMALLINT"},
	{"dropoff_borough_name", "STRING"},
	{"dropoff_tract_code", "STRING"},
	{"dropoff_district_class", "STRING"},
	{"dropoff_neighborhood_code", "STRING"},
	{"dropoff_neighborhood_name", "STRING"},
	{"dropoff_ward", "INT"},
}

var metadataKeys = []string{
	"SOURCE_ROWS", "SOURCE_PICKUP_MIN_EPOCH", "SOURCE_PICKUP_MAX_EPOCH",
	"SOURCE_DROPOFF_MIN_EPOCH", "SOURCE_DROPOFF_MAX_EPOCH", "SEED_EPOCH", "SHIFT_SECONDS",
}

type seeder struct {
	baseURL, database, user, password, dataDir string
	client                                     *http.Client
	log                                        io.Writer
}

type sqlOutput struct {
	AffectedRows *int64 `json:"affectedrows"`
	Records      *struct {
		Rows [][]json.Number `json:"rows"`
	} `json:"records"`
}

func main() {
	s := &seeder{
		baseURL:  envOr("GREPTIMEDB_URL", "http://greptimedb:4000"), //nolint:revive // developer-environment default
		database: envOr("GREPTIMEDB_DATABASE", "public"),
		user:     envOr("GREPTIMEDB_SEED_USER", "seeder"),
		password: envOr("GREPTIMEDB_SEED_PASSWORD", "trickster-dev-seed"),
		dataDir:  envOr("GREPTIMEDB_SEED_DATA", "/seed-data"),
		client:   &http.Client{Timeout: 2 * time.Minute},
		log:      os.Stdout,
	}
	if err := s.run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "greptime seed:", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (s *seeder) sql(query string) (sqlOutput, error) {
	form := url.Values{"db": {s.database}, "sql": {query}}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(s.baseURL, "/")+"/v1/sql", strings.NewReader(form.Encode()))
	if err != nil {
		return sqlOutput{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.user, s.password)
	resp, err := s.client.Do(req)
	if err != nil {
		return sqlOutput{}, err
	}
	defer resp.Body.Close()
	var doc struct {
		Output []sqlOutput `json:"output"`
		Code   int         `json:"code"`
		Error  string      `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return sqlOutput{}, fmt.Errorf("SQL HTTP %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || doc.Code != 0 || doc.Error != "" {
		return sqlOutput{}, fmt.Errorf("SQL HTTP %d code %d: %s", resp.StatusCode, doc.Code, doc.Error)
	}
	if len(doc.Output) != 1 {
		return sqlOutput{}, fmt.Errorf("expected one SQL result, got %d", len(doc.Output))
	}
	return doc.Output[0], nil
}

func readMetadata(path string) (map[string]int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := make(map[string]int64)
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid metadata line %q", line)
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", key, err)
		}
		if _, exists := m[key]; exists {
			return nil, fmt.Errorf("duplicate metadata key %s", key)
		}
		m[key] = n
	}
	for _, key := range metadataKeys {
		if _, ok := m[key]; !ok {
			return nil, fmt.Errorf("missing metadata key %s", key)
		}
	}
	if m["SOURCE_ROWS"] <= 0 {
		return nil, errors.New("SOURCE_ROWS must be positive")
	}
	for _, kind := range []string{"PICKUP", "DROPOFF"} {
		minTime, maxTime := m["SOURCE_"+kind+"_MIN_EPOCH"], m["SOURCE_"+kind+"_MAX_EPOCH"]
		if minTime > maxTime {
			return nil, fmt.Errorf("reversed %s bounds", kind)
		}
		for _, v := range []int64{minTime, maxTime} {
			if _, err := shiftEpoch(v, m["SHIFT_SECONDS"]); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

func shiftEpoch(epoch, shift int64) (time.Time, error) {
	if shift > 0 && epoch > math.MaxInt64-shift || shift < 0 && epoch < math.MinInt64-shift {
		return time.Time{}, errors.New("timestamp shift overflows")
	}
	t := time.Unix(epoch+shift, 0).UTC()
	if t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, errors.New("timestamp outside supported calendar range")
	}
	return t, nil
}

func sourceNames() []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.name
	}
	return names
}

func createTableSQL() string {
	defs := make([]string, 0, len(columns)+3)
	for _, c := range columns {
		def := `"` + c.name + `" ` + c.kind
		if c.name == "pickup_datetime" {
			def += " NOT NULL TIME INDEX"
		} else if c.name == "trip_id" || c.name == "vendor_id" || c.name == "cab_type" {
			def += " NOT NULL"
		}
		defs = append(defs, def)
	}
	defs = append(defs, "pickup_epoch BIGINT NOT NULL", "PRIMARY KEY (cab_type, vendor_id)")
	return "CREATE TABLE trips (" + strings.Join(defs, ",") + ") WITH (append_mode='true')"
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func rowSQL(fields []string, shift int64) (string, error) {
	if len(fields) != len(columns) {
		return "", fmt.Errorf("expected %d columns, got %d", len(columns), len(fields))
	}
	values := make([]string, len(fields)+1)
	var pickup time.Time
	for i, c := range columns {
		value := fields[i]
		switch c.kind {
		case "STRING":
			values[i] = quote(value)
		case "DATE":
			// Date fields are regenerated from the shifted datetime next to them.
			continue
		case "TIMESTAMP(6)":
			if value == "" && c.name == "dropoff_datetime" {
				values[i-1], values[i] = "NULL", "NULL"
				continue
			}
			t, err := time.Parse("2006-01-02 15:04:05", value)
			if err != nil {
				return "", fmt.Errorf("%s: %w", c.name, err)
			}
			t, err = shiftEpoch(t.Unix(), shift)
			if err != nil {
				return "", err
			}
			values[i-1] = quote(t.Format(time.DateOnly))
			values[i] = quote(t.Format(time.RFC3339))
			if c.name == "pickup_datetime" {
				pickup = t
			}
		default:
			if value == "" {
				values[i] = "NULL"
				continue
			}
			if c.kind == "FLOAT" || c.kind == "DOUBLE" {
				n, err := strconv.ParseFloat(value, 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return "", fmt.Errorf("invalid %s: %q", c.name, value)
				}
				values[i] = strconv.FormatFloat(n, 'g', -1, 64)
			} else {
				bits := 64
				if c.kind == "SMALLINT" {
					bits = 16
				} else if c.kind == "INT" {
					bits = 32
				}
				n, err := strconv.ParseInt(value, 10, bits)
				if err != nil {
					return "", fmt.Errorf("%s: %w", c.name, err)
				}
				values[i] = strconv.FormatInt(n, 10)
			}
		}
	}
	values[len(fields)] = strconv.FormatInt(pickup.Unix(), 10)
	return "(" + strings.Join(values, ",") + ")", nil
}

func (s *seeder) loadFile(name string, shift int64) (int64, error) {
	f, err := os.Open(filepath.Join(s.dataDir, name))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	scan := bufio.NewScanner(zr)
	scan.Buffer(make([]byte, 64<<10), 1<<20)
	if !scan.Scan() || scan.Text() != strings.Join(sourceNames(), "\t") {
		return 0, fmt.Errorf("%s: missing or mismatched TSV header", name)
	}
	const batchSize = 1000
	batch := make([]string, 0, batchSize)
	var rows int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Do not retry an INSERT: append mode would duplicate a committed batch
		// whose response was lost. A failed seed must restart from DROP TABLE.
		out, err := s.sql(`INSERT INTO trips ("` + strings.Join(sourceNames(), `","`) + `",pickup_epoch) VALUES ` + strings.Join(batch, ","))
		if err != nil {
			return err
		}
		if out.AffectedRows == nil || *out.AffectedRows != int64(len(batch)) {
			return errors.New("INSERT affected-row count does not match batch")
		}
		rows += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	for scan.Scan() {
		row, err := rowSQL(strings.Split(scan.Text(), "\t"), shift)
		if err != nil {
			return rows, fmt.Errorf("%s row %d: %w", name, rows+int64(len(batch))+1, err)
		}
		batch = append(batch, row)
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return rows, err
			}
		}
	}
	if err := scan.Err(); err != nil {
		return rows, fmt.Errorf("%s: %w", name, err)
	}
	if err := flush(); err != nil {
		return rows, err
	}
	return rows, nil
}

func (s *seeder) run() error {
	m, err := readMetadata(filepath.Join(s.dataDir, "seed-window.env"))
	if err != nil {
		return err
	}
	for _, q := range []string{"DROP TABLE IF EXISTS trips_15m", "DROP TABLE IF EXISTS trips", createTableSQL()} {
		if _, err := s.sql(q); err != nil {
			return err
		}
	}
	for _, name := range []string{"trips_1.gz", "trips_2.gz"} {
		if _, err := s.loadFile(name, m["SHIFT_SECONDS"]); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`CREATE TABLE trips_15m ("bucket" TIMESTAMP(6) TIME INDEX, cab_type STRING, trips BIGINT, total_amount_sum DOUBLE, PRIMARY KEY (cab_type))`,
		`INSERT INTO trips_15m SELECT date_bin(INTERVAL '15 minutes', pickup_datetime) AS "bucket", cab_type, count(*), sum(total_amount) FROM trips GROUP BY 1, 2`,
	} {
		if _, err := s.sql(q); err != nil {
			return err
		}
	}
	return s.validate(m)
}

func (s *seeder) validate(m map[string]int64) error {
	out, err := s.sql(`SELECT count(*),
 CAST(extract(epoch FROM min(pickup_datetime)) AS BIGINT),
 CAST(extract(epoch FROM max(pickup_datetime)) AS BIGINT),
 CAST(extract(epoch FROM min(dropoff_datetime)) AS BIGINT),
 CAST(extract(epoch FROM max(dropoff_datetime)) AS BIGINT),
 count(*) FILTER (WHERE pickup_date <> CAST(pickup_datetime AS DATE)),
 count(*) FILTER (WHERE dropoff_datetime IS NOT NULL AND dropoff_date IS DISTINCT FROM CAST(dropoff_datetime AS DATE)),
 count(*) FILTER (WHERE pickup_epoch <> CAST(extract(epoch FROM pickup_datetime) AS BIGINT)),
 (SELECT CAST(sum(trips) AS BIGINT) FROM trips_15m)
 FROM trips`)
	if err != nil {
		return err
	}
	want := []int64{
		m["SOURCE_ROWS"],
		m["SOURCE_PICKUP_MIN_EPOCH"] + m["SHIFT_SECONDS"], m["SOURCE_PICKUP_MAX_EPOCH"] + m["SHIFT_SECONDS"],
		m["SOURCE_DROPOFF_MIN_EPOCH"] + m["SHIFT_SECONDS"], m["SOURCE_DROPOFF_MAX_EPOCH"] + m["SHIFT_SECONDS"],
		0, 0, 0, m["SOURCE_ROWS"],
	}
	if out.Records == nil || len(out.Records.Rows) != 1 || len(out.Records.Rows[0]) != len(want) {
		return errors.New("invalid seed validation result")
	}
	for i, expected := range want {
		got, err := out.Records.Rows[0][i].Int64()
		if err != nil || got != expected {
			return fmt.Errorf("seed validation column %d: got %s, want %d", i, out.Records.Rows[0][i], expected)
		}
	}
	midpoint := want[1] + (want[2]-want[1])/2
	if midpoint != m["SEED_EPOCH"] {
		return errors.New("seed window is not centered on SEED_EPOCH")
	}
	_, _ = fmt.Fprintf(s.log, "seed complete: %d rows, pickup window %d..%d\n", want[0], want[1], want[2])
	return nil
}
