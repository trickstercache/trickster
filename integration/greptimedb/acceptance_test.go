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

package greptimedb_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const environment = "../../docs/developer/environment/docker-compose-data"

type dashboard struct {
	Panels []struct {
		ID      int              `json:"id"`
		Title   string           `json:"title"`
		Targets []map[string]any `json:"targets"`
	} `json:"panels"`
}

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type report struct {
	StartedAt time.Time         `json:"started_at"`
	GoVersion string            `json:"go_version"`
	BuildNote string            `json:"operator_build_note,omitempty"`
	From      time.Time         `json:"sql_from"`
	To        time.Time         `json:"sql_to"`
	Hashes    map[string]string `json:"input_sha256"`
	Facts     map[string]any    `json:"observed_facts"`
	Checks    []check           `json:"checks"`
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func readSeed(raw []byte) (map[string]int64, error) {
	keys := []string{
		"SOURCE_ROWS", "SOURCE_PICKUP_MIN_EPOCH", "SOURCE_PICKUP_MAX_EPOCH",
		"SOURCE_DROPOFF_MIN_EPOCH", "SOURCE_DROPOFF_MAX_EPOCH", "SEED_EPOCH", "SHIFT_SECONDS",
	}
	m := make(map[string]int64, len(keys))
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid seed metadata line %q", line)
		}
		if _, ok := m[key]; ok {
			return nil, fmt.Errorf("duplicate seed key %s", key)
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid seed value for %s: %w", key, err)
		}
		m[key] = n
	}
	for _, key := range keys {
		if _, ok := m[key]; !ok {
			return nil, fmt.Errorf("missing seed key %s", key)
		}
	}
	if len(m) != len(keys) || m["SOURCE_ROWS"] <= 0 || m["SEED_EPOCH"] <= 0 ||
		m["SOURCE_PICKUP_MIN_EPOCH"] > m["SOURCE_PICKUP_MAX_EPOCH"] ||
		m["SOURCE_DROPOFF_MIN_EPOCH"] > m["SOURCE_DROPOFF_MAX_EPOCH"] {
		return nil, fmt.Errorf("invalid seed metadata")
	}
	return m, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (g grafanaClient) query(targets []map[string]any, uid, kind string, from, to time.Time, step time.Duration) (queryResponse, error) {
	queries := make([]map[string]any, len(targets))
	refs := make([]string, len(targets))
	for i, target := range targets {
		q := make(map[string]any, len(target)+3)
		for key, value := range target {
			q[key] = value
		}
		q["datasource"] = map[string]string{"type": kind, "uid": uid}
		q["intervalMs"] = step.Milliseconds()
		q["maxDataPoints"] = int(to.Sub(from)/step) + 1
		queries[i] = q
		refs[i], _ = q["refId"].(string)
	}
	var doc queryResponse
	err := g.request(http.MethodPost, "/api/ds/query", map[string]any{
		"queries": queries, "from": strconv.FormatInt(from.UnixMilli(), 10), "to": strconv.FormatInt(to.UnixMilli(), 10),
	}, &doc)
	if err == nil {
		err = validateResponse(doc, refs)
	}
	return doc, err
}

func TestDirectEnvironment(t *testing.T) {
	if os.Getenv("TRICKSTER_GREPTIMEDB_ACCEPTANCE") != "1" {
		t.Skip("set TRICKSTER_GREPTIMEDB_ACCEPTANCE=1 for the read-only live suite")
	}
	out := os.Getenv("GREPTIMEDB_REPORT_DIR")
	if out == "" {
		t.Fatal("GREPTIMEDB_REPORT_DIR is required; use make developer-greptimedb-check")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	// Never overwrite an earlier run, including its failures.
	f, err := os.OpenFile(filepath.Join(out, "report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r := report{
		StartedAt: time.Now().UTC(), GoVersion: runtime.Version(),
		BuildNote: os.Getenv("GREPTIMEDB_BUILD_NOTE"), Hashes: map[string]string{}, Facts: map[string]any{},
	}
	t.Cleanup(func() {
		for _, capability := range []struct{ probe, name, detail string }{
			{"pg_cancel_capability", "pg_cancel_support", "Zero cancellation credentials; upstream uses a no-op cancellation handler"},
			{"pg_transaction_compatibility", "pg_transaction_support", "BEGIN/ROLLBACK are compatibility stubs; upstream warns that transactions are unsupported"},
		} {
			c := check{capability.name, "UNVERIFIED", "Capability probe did not pass"}
			for _, probe := range r.Checks {
				if probe.Name == capability.probe && probe.Status == "PASS" {
					c.Status, c.Detail = "UNSUPPORTED", capability.detail
				}
			}
			r.Checks = append(r.Checks, c)
		}
		for _, name := range []string{
			"actual Grafana PostgreSQL wire capture (not the pgconn probe)",
			"desktop/mobile visual inspection of every data panel",
			"existing Prometheus dashboard rendered against the GreptimeDB PromQL datasource",
			"full-profile reseed and existing seed targets",
			"populated GreptimeDB Trickster performance panels (later provider/cache phases)",
			"Trickster provider, cache behavior and complete issue #1150 acceptance",
		} {
			r.Checks = append(r.Checks, check{name, "UNVERIFIED", "Outside this read-only direct-environment suite"})
		}
		if err := writeJSON(filepath.Join(out, "report.json"), r); err != nil {
			t.Error(err)
		}
		t.Logf("review report: %s", filepath.Join(out, "report.json"))
	})
	run := func(name string, fn func() error) {
		t.Run(name, func(t *testing.T) {
			c := check{Name: name, Status: "PASS"}
			if err := fn(); err != nil {
				c.Status, c.Detail = "FAIL", err.Error()
				t.Error(err)
			}
			r.Checks = append(r.Checks, c)
		})
	}
	read := func(name, path string, dst any) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		r.Hashes[name] = fmt.Sprintf("%x", sha256.Sum256(b))
		return json.Unmarshal(b, dst)
	}
	var seed map[string]int64
	var greptime, timescale dashboard
	var setupErr error
	run("inputs", func() error {
		raw, err := os.ReadFile(filepath.Join(environment, "seed-data", "seed-window.env"))
		if err == nil {
			seed, err = readSeed(raw)
			r.Hashes["seed-window.env"] = fmt.Sprintf("%x", sha256.Sum256(raw))
		}
		if err == nil {
			err = read("greptimedb-dashboard", filepath.Join(environment, "dashboards", "trickster-greptimedb.json"), &greptime)
		}
		if err == nil {
			err = read("timescaledb-dashboard", filepath.Join(environment, "dashboards", "trickster-timescaledb.json"), &timescale)
		}
		setupErr = err
		return err
	})
	if setupErr != nil {
		return
	}
	r.To = time.Unix(seed["SEED_EPOCH"], 0).UTC().Truncate(24 * time.Hour)
	r.From = r.To.Add(-48 * time.Hour)
	r.Facts["seed_metadata"] = seed
	g := grafanaClient{strings.TrimRight(envOr("GRAFANA_URL", "http://127.0.0.1:3000"), "/"), &http.Client{Timeout: 30 * time.Second}}
	const sqlKind = "grafana-postgresql-datasource"
	gtUID := envOr("GREPTIMEDB_SQL_UID", "ds_greptimedb_direct")
	tsUID := "ds_timescaledb_direct"
	promUID := envOr("GREPTIMEDB_PROM_UID", "ds_greptimedb_prom_direct")
	run("grafana_version", func() error {
		var health struct{ Version, Database string }
		if err := g.request(http.MethodGet, "/api/health", nil, &health); err != nil {
			return err
		}
		r.Facts["grafana_version"] = health.Version
		if health.Version == "" || health.Database != "ok" {
			return fmt.Errorf("Grafana is not healthy: %+v", health)
		}
		return nil
	})
	for _, uid := range []string{gtUID, tsUID, promUID} {
		run("health_"+uid, func() error {
			var health struct{ Status, Message string }
			if err := g.request(http.MethodGet, "/api/datasources/uid/"+url.PathEscape(uid)+"/health", nil, &health); err != nil {
				return err
			}
			if health.Status != "OK" {
				return fmt.Errorf("datasource status %q: %s", health.Status, health.Message)
			}
			return nil
		})
	}
	querySQL := func(uid, sql string) (queryResponse, error) {
		return g.query([]map[string]any{{"refId": "A", "format": "table", "rawQuery": true, "rawSql": sql}}, uid, sqlKind, r.From, r.To, 5*time.Minute)
	}
	run("grafana_pg_type_query", func() error {
		doc, err := querySQL(gtUID, pgTypeQuery)
		if err != nil {
			return err
		}
		r.Facts["grafana_pg_type_frame_not_raw_wire"] = doc.Results["A"].Frames
		frames := doc.Results["A"].Frames
		if len(frames) != 1 || len(frames[0].Schema.Fields) != 10 || len(frames[0].Data.Values[0]) != 1 {
			return fmt.Errorf("unexpected Grafana typed-query frame")
		}
		return writeJSON(filepath.Join(out, "grafana-type-query.json"), doc)
	})
	for _, uid := range []string{gtUID, tsUID} {
		run("version_"+uid, func() error {
			doc, err := querySQL(uid, "SELECT version() AS version")
			if err == nil {
				r.Facts["version_"+uid] = doc.Results["A"].Frames
			}
			return err
		})
	}
	run("seed_count_and_bounds", func() error {
		sql := "SELECT COUNT(*) AS rows, MIN(pickup_epoch) AS first, MAX(pickup_epoch) AS last FROM trips"
		want := []any{
			json.Number(strconv.FormatInt(seed["SOURCE_ROWS"], 10)),
			json.Number(strconv.FormatInt(seed["SOURCE_PICKUP_MIN_EPOCH"]+seed["SHIFT_SECONDS"], 10)),
			json.Number(strconv.FormatInt(seed["SOURCE_PICKUP_MAX_EPOCH"]+seed["SHIFT_SECONDS"], 10)),
		}
		for _, uid := range []string{gtUID, tsUID} {
			doc, err := querySQL(uid, sql)
			if err != nil {
				return err
			}
			if err := writeJSON(filepath.Join(out, "seed-"+uid+".json"), doc); err != nil {
				return err
			}
			frames := doc.Results["A"].Frames
			if len(frames) != 1 || len(frames[0].Data.Values) != len(want) {
				return fmt.Errorf("%s: unexpected seed result shape", uid)
			}
			for i, value := range want {
				if len(frames[0].Data.Values[i]) != 1 || frames[0].Data.Values[i][0] != value {
					return fmt.Errorf("%s: seed field %d = %v, want %v", uid, i, frames[0].Data.Values[i], value)
				}
			}
		}
		return nil
	})
	for id := 1; id <= 6; id++ {
		run(fmt.Sprintf("panel_%d", id), func() error {
			var docs []queryResponse
			for i, dashboard := range []dashboard{timescale, greptime} {
				var targets []map[string]any
				for _, p := range dashboard.Panels {
					if p.ID == id {
						targets = p.Targets
					}
				}
				if len(targets) == 0 {
					return fmt.Errorf("missing panel %d targets", id)
				}
				uid := []string{tsUID, gtUID}[i]
				doc, err := g.query(targets, uid, sqlKind, r.From, r.To, 5*time.Minute)
				if writeErr := writeJSON(filepath.Join(out, fmt.Sprintf("panel-%d-%s.json", id, uid)), doc); writeErr != nil {
					return writeErr
				}
				if err != nil {
					return fmt.Errorf("%s: %w", uid, err)
				}
				docs = append(docs, doc)
			}
			if id == 6 {
				rounded, err := compareFrames(docs[0], docs[1], true)
				r.Facts["panel_6_percentage_rounding"] = map[string]any{
					"differing_cells": rounded, "maximum_float64_steps": 1,
					"field": "card_use_rate", "other_fields": "exact",
				}
				return err
			}
			return compareResponses(docs[0], docs[1])
		})
	}
	run("promql_sample_grid", func() error {
		// Exclude the newest samples so remote-write lag cannot change the window.
		to := r.StartedAt.Add(-time.Minute).Truncate(15 * time.Second)
		from := to.Add(-2 * time.Minute)
		r.Facts["promql_window"] = map[string]any{"from": from, "to": to, "step_seconds": 15}
		targets := []map[string]any{{"refId": "A", "expr": `up{job="prometheus"}`, "range": true, "instant": false, "interval": "15s"}}
		var docs []queryResponse
		for _, uid := range []string{"ds_prom_direct", promUID} {
			doc, err := g.query(targets, uid, "prometheus", from, to, 15*time.Second)
			if writeErr := writeJSON(filepath.Join(out, "promql-"+uid+".json"), doc); writeErr != nil {
				return writeErr
			}
			if err != nil {
				return err
			}
			frames := doc.Results["A"].Frames
			if len(frames) != 1 || len(frames[0].Data.Values) != 2 || len(frames[0].Data.Values[0]) != 9 {
				return fmt.Errorf("%s: expected one complete nine-sample series", uid)
			}
			for i, value := range frames[0].Data.Values[1] {
				wantTime := json.Number(strconv.FormatInt(from.Add(time.Duration(i)*15*time.Second).UnixMilli(), 10))
				if value != json.Number("1") || frames[0].Data.Values[0][i] != wantTime {
					return fmt.Errorf("%s: incomplete or unhealthy scrape at sample %d", uid, i)
				}
			}
			docs = append(docs, doc)
		}
		return compareResponses(docs[0], docs[1])
	})
	protocolChecks(run, &r)
}

func TestReadSeed(t *testing.T) {
	valid := "SOURCE_ROWS=2\nSOURCE_PICKUP_MIN_EPOCH=1\nSOURCE_PICKUP_MAX_EPOCH=2\nSOURCE_DROPOFF_MIN_EPOCH=2\nSOURCE_DROPOFF_MAX_EPOCH=3\nSEED_EPOCH=1800000000\nSHIFT_SECONDS=1799999999\n"
	for _, tt := range []struct {
		name, raw string
		ok        bool
	}{
		{"valid", valid, true},
		{"crlf", strings.ReplaceAll(valid, "\n", "\r\n"), true},
		{"missing", strings.Replace(valid, "SEED_EPOCH=1800000000\n", "", 1), false},
		{"duplicate", valid + "SEED_EPOCH=1\n", false},
		{"empty_rows", strings.Replace(valid, "SOURCE_ROWS=2", "SOURCE_ROWS=0", 1), false},
		{"shell_value", strings.Replace(valid, "SOURCE_ROWS=2", "SOURCE_ROWS=$(date)", 1), false},
		{"unknown_key", valid + "EXTRA=1\n", false},
		{"reversed_bounds", strings.Replace(valid, "SOURCE_PICKUP_MAX_EPOCH=2", "SOURCE_PICKUP_MAX_EPOCH=0", 1), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readSeed([]byte(tt.raw))
			if (err == nil) != tt.ok {
				t.Fatalf("error = %v, want success %v", err, tt.ok)
			}
		})
	}
}
