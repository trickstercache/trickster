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

// Command druidseed loads the shared generated trips seed files into Apache
// Druid through its native batch API, shifting __time by the offset recorded
// in seed-window.env so the data stays centered on the seed instant.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var columns = []string{
	"trip_id", "vendor_id", "pickup_date", "pickup_datetime", "dropoff_date",
	"dropoff_datetime", "store_and_fwd_flag", "rate_code_id", "pickup_longitude",
	"pickup_latitude", "dropoff_longitude", "dropoff_latitude", "passenger_count",
	"trip_distance", "fare_amount", "extra", "transit_tax", "tip_amount",
	"tolls_amount", "ehail_fee", "improvement_surcharge", "total_amount",
	"payment_type", "trip_type", "pickup", "dropoff", "cab_type",
	"pickup_zone_gid", "pickup_tract_label", "pickup_borough_code",
	"pickup_borough_name", "pickup_tract_code", "pickup_district_class",
	"pickup_neighborhood_code", "pickup_neighborhood_name", "pickup_ward",
	"dropoff_zone_gid", "dropoff_tract_label", "dropoff_borough_code",
	"dropoff_borough_name", "dropoff_tract_code", "dropoff_district_class",
	"dropoff_neighborhood_code", "dropoff_neighborhood_name", "dropoff_ward",
}

var dimensions = []string{
	"cab_type", "pickup_neighborhood_name", "dropoff_neighborhood_name",
	"payment_type", "vendor_id",
}

var requiredKeys = []string{
	"SOURCE_ROWS", "SOURCE_PICKUP_MIN_EPOCH", "SOURCE_PICKUP_MAX_EPOCH",
	"SEED_EPOCH", "SHIFT_SECONDS",
}

var seedFiles = []string{"trips_1.gz", "trips_2.gz"}

type seeder struct {
	druidURL   string
	datasource string
	dataDir    string
	timeout    time.Duration
	poll       time.Duration // between task status checks
	retryBase  time.Duration // request retry back-off unit
	client     *http.Client
	log        io.Writer
}

func main() {
	s := newSeederFromEnv()
	if err := s.run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "druid seed:", err)
		os.Exit(1)
	}
}

func newSeederFromEnv() *seeder {
	timeout := 3600.0
	if v, err := strconv.ParseFloat(os.Getenv("DRUID_SEED_TIMEOUT"), 64); err == nil {
		timeout = v
	}
	return &seeder{
		druidURL:   strings.TrimRight(envOr("DRUID_URL", "http://druid:8888"), "/"), //nolint:revive // developer-environment default
		datasource: envOr("DRUID_DATASOURCE", "trips"),
		dataDir:    envOr("DRUID_SEED_DATA", "/seed-data"),
		timeout:    time.Duration(timeout * float64(time.Second)),
		poll:       5 * time.Second,
		retryBase:  250 * time.Millisecond,
		client:     &http.Client{Timeout: 15 * time.Second},
		log:        os.Stdout,
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (s *seeder) run() error {
	meta, err := s.readMetadata()
	if err != nil {
		return err
	}
	if err := s.markExistingSegmentsUnused(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(s.log, "submitting Druid batch seed: rows=%d shift_seconds=%d\n",
		meta["SOURCE_ROWS"], meta["SHIFT_SECONDS"])
	response, err := s.requestJSON("/druid/indexer/v1/task", s.ingestionSpec(meta), nil)
	if err != nil {
		return err
	}
	doc, _ := response.(map[string]any)
	taskID, _ := doc["task"].(string)
	if taskID == "" {
		return fmt.Errorf("druid did not return a task id: %v", response)
	}
	if err := s.waitForTask(taskID); err != nil {
		return err
	}
	return s.validate(meta)
}

// readMetadata parses the integer-only seed-window.env and checks the files.
func (s *seeder) readMetadata() (map[string]int64, error) {
	path := filepath.Join(s.dataDir, "seed-window.env")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("missing %s; run seed_data_generate first", path)
	}
	values := map[string]int64{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid %s in %s", key, path)
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
		return nil, fmt.Errorf("missing metadata fields: %s", strings.Join(missing, ", "))
	}
	if values["SOURCE_ROWS"] <= 0 {
		return nil, errors.New("SOURCE_ROWS must be positive")
	}
	for _, name := range seedFiles {
		st, err := os.Stat(filepath.Join(s.dataDir, name))
		if err != nil || st.Size() == 0 {
			return nil, fmt.Errorf("missing or empty source file %s", filepath.Join(s.dataDir, name))
		}
	}
	return values, nil
}

// requestJSON calls the Druid API with retries on transient failures. HTTP
// codes listed in tolerate return nil instead of failing.
func (s *seeder) requestJSON(path string, payload any, tolerate map[int]bool) (any, error) {
	var body []byte
	method := http.MethodGet
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return nil, err
		}
		method = http.MethodPost
	}
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		req, err := http.NewRequest(method, s.druidURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := s.client.Do(req)
		if err == nil {
			raw, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			switch {
			case readErr != nil:
				err = readErr
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				if len(raw) == 0 {
					return nil, nil
				}
				var decoded any
				if err = json.Unmarshal(raw, &decoded); err == nil {
					return decoded, nil
				}
			case tolerate[resp.StatusCode]:
				return nil, nil
			case !retryable(resp.StatusCode):
				detail := string(raw)
				if len(detail) > 400 {
					detail = detail[:400]
				}
				return nil, fmt.Errorf("druid API %s returned HTTP %d: %s", path, resp.StatusCode, detail)
			default:
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		lastErr = err
		if attempt < 30 {
			time.Sleep(min(8*s.retryBase, time.Duration(attempt)*s.retryBase))
		}
	}
	return nil, fmt.Errorf("druid API %s unavailable: %w", path, lastErr)
}

func retryable(code int) bool {
	switch code {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func stringList(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		str, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, str)
	}
	return out, true
}

func (s *seeder) markExistingSegmentsUnused() error {
	response, err := s.requestJSON("/druid/coordinator/v1/metadata/datasources", nil, nil)
	if err != nil {
		return err
	}
	datasources, ok := stringList(response)
	if !ok {
		return fmt.Errorf("unexpected datasource metadata response: %v", response)
	}
	found := false
	for _, name := range datasources {
		if name == s.datasource {
			found = true
		}
	}
	if !found {
		return nil
	}
	encoded := url.PathEscape(s.datasource)
	response, err = s.requestJSON("/druid/coordinator/v1/metadata/datasources/"+encoded+"/segments", nil, nil)
	if err != nil {
		return err
	}
	segments, ok := stringList(response)
	if !ok {
		return fmt.Errorf("unexpected segment metadata response: %v", response)
	}
	if len(segments) == 0 {
		return nil
	}
	response, err = s.requestJSON("/druid/indexer/v1/datasources/"+encoded+"/markUnused",
		map[string]any{"segmentIds": segments}, nil)
	if err != nil {
		return err
	}
	if response != nil {
		if _, ok := response.(map[string]any); !ok {
			return fmt.Errorf("unexpected markUnused response: %v", response)
		}
	}
	_, _ = fmt.Fprintf(s.log, "marked %d existing Druid segments unused\n", len(segments))
	return nil
}

func isoDay(epoch int64) string {
	return time.Unix(epoch, 0).UTC().Truncate(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
}

// ingestionSpec builds the index_parallel task. __time is populated from
// pickup_datetime before transforms run, so shadowing it applies the shift.
func (s *seeder) ingestionSpec(meta map[string]int64) map[string]any {
	shiftMillis := meta["SHIFT_SECONDS"] * 1000
	targetMin := meta["SOURCE_PICKUP_MIN_EPOCH"] + meta["SHIFT_SECONDS"]
	targetMax := meta["SOURCE_PICKUP_MAX_EPOCH"] + meta["SHIFT_SECONDS"]
	interval := isoDay(targetMin) + "/" + isoDay(targetMax+86400)
	shift := fmt.Sprintf("__time + %d", shiftMillis)
	if shiftMillis < 0 {
		shift = fmt.Sprintf("__time - %d", -shiftMillis)
	}
	files := make([]string, len(seedFiles))
	for i, name := range seedFiles {
		files[i] = filepath.Join(s.dataDir, name)
	}
	return map[string]any{
		"type": "index_parallel",
		"spec": map[string]any{
			"dataSchema": map[string]any{
				"dataSource":    s.datasource,
				"timestampSpec": map[string]any{"column": "pickup_datetime", "format": "auto"},
				"transformSpec": map[string]any{
					"transforms": []map[string]any{
						{"type": "expression", "name": "__time", "expression": shift},
					},
				},
				"dimensionsSpec": map[string]any{"dimensions": dimensions},
				"metricsSpec": []map[string]any{
					{"type": "count", "name": "trip_count"},
					{"type": "doubleSum", "name": "fare_amount", "fieldName": "fare_amount"},
					{"type": "doubleSum", "name": "tip_amount", "fieldName": "tip_amount"},
					{"type": "doubleSum", "name": "total_amount", "fieldName": "total_amount"},
					{"type": "doubleSum", "name": "trip_distance", "fieldName": "trip_distance"},
					{"type": "longSum", "name": "passenger_count", "fieldName": "passenger_count"},
				},
				"granularitySpec": map[string]any{
					"type":               "uniform",
					"segmentGranularity": "DAY",
					"queryGranularity":   "NONE",
					"rollup":             false,
					"intervals":          []string{interval},
				},
			},
			"ioConfig": map[string]any{
				"type":        "index_parallel",
				"inputSource": map[string]any{"type": "local", "files": files},
				"inputFormat": map[string]any{
					"type":            "tsv",
					"columns":         columns,
					"skipHeaderRows":  1,
					"tryParseNumbers": true,
				},
				"appendToExisting": false,
				"dropExisting":     true,
			},
			"tuningConfig": map[string]any{
				"type":                     "index_parallel",
				"partitionsSpec":           map[string]any{"type": "dynamic"},
				"maxNumConcurrentSubTasks": 1,
				"maxRetry":                 2,
			},
		},
		"context": map[string]any{"useLineageBasedSegmentAllocation": false},
	}
}

func (s *seeder) waitForTask(taskID string) error {
	deadline := time.Now().Add(s.timeout)
	for time.Now().Before(deadline) {
		document, err := s.requestJSON("/druid/indexer/v1/task/"+taskID+"/status", nil, nil)
		if err != nil {
			return err
		}
		state := "unknown"
		if doc, ok := document.(map[string]any); ok {
			if status, ok := doc["status"].(map[string]any); ok {
				if v, ok := status["status"].(string); ok && v != "" {
					state = v
				}
			}
		}
		_, _ = fmt.Fprintf(s.log, "druid seed task %s: %s\n", taskID, state)
		switch state {
		case "SUCCESS":
			return nil
		case "FAILED", "CANCELED":
			report, _ := s.requestJSON("/druid/indexer/v1/task/"+taskID+"/reports", nil, nil)
			text, _ := json.Marshal(report)
			if len(text) > 1000 {
				text = text[:1000]
			}
			return fmt.Errorf("batch task %s ended %s: %s", taskID, state, text)
		}
		time.Sleep(s.poll)
	}
	return fmt.Errorf("batch task %s did not finish before timeout", taskID)
}

// validate polls Druid SQL until the row count matches, then checks the
// shifted bounds. The datasource is briefly unknown to the broker between
// marking old segments unused and the new ones publishing, so 400 is tolerated.
func (s *seeder) validate(meta map[string]int64) error {
	query := map[string]any{
		"query":        fmt.Sprintf(`SELECT COUNT(*) AS "rows", MIN(__time) AS "min_time", MAX(__time) AS "max_time" FROM "%s"`, s.datasource),
		"resultFormat": "object",
	}
	deadline := time.Now().Add(36 * s.poll)
	for time.Now().Before(deadline) {
		response, err := s.requestJSON("/druid/v2/sql", query, map[int]bool{400: true})
		if err != nil {
			return err
		}
		if rows, ok := response.([]any); ok && len(rows) > 0 {
			if row, ok := rows[0].(map[string]any); ok {
				count, _ := toInt(row["rows"])
				if count == meta["SOURCE_ROWS"] {
					expectedMin := meta["SOURCE_PICKUP_MIN_EPOCH"] + meta["SHIFT_SECONDS"]
					expectedMax := meta["SOURCE_PICKUP_MAX_EPOCH"] + meta["SHIFT_SECONDS"]
					minEpoch, errMin := parseSQLEpoch(row["min_time"])
					maxEpoch, errMax := parseSQLEpoch(row["max_time"])
					if errMin == nil && errMax == nil && (minEpoch != expectedMin || maxEpoch != expectedMax) {
						return fmt.Errorf("timestamp bounds mismatch: got %d..%d, expected %d..%d",
							minEpoch, maxEpoch, expectedMin, expectedMax)
					}
					_, _ = fmt.Fprintf(s.log, "druid seed complete: %d rows\n", count)
					return nil
				}
			}
		}
		time.Sleep(3 * s.poll / 5)
	}
	return fmt.Errorf("expected %d rows in %s", meta["SOURCE_ROWS"], s.datasource)
}

func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// parseSQLEpoch accepts Druid's ISO text or numeric millisecond timestamps.
func parseSQLEpoch(v any) (int64, error) {
	switch t := v.(type) {
	case float64:
		return int64(t) / 1000, nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed.Unix(), nil
			}
		}
	}
	return 0, fmt.Errorf("unsupported timestamp value %v", v)
}
