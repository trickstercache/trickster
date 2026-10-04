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

// Command vmseed loads the developer VictoriaMetrics with the trips history
// that devorigin backfilled as OpenMetrics, plus small deterministic fixtures
// for MetricsQL edge cases and the Graphite APIs, then verifies every count.
//
// It never deletes series: after a delete, VictoriaMetrics v1.153.0 can hide
// re-imported samples on dates the deleted series covered. Trips history is
// imported only into storage without it, and fixtures are extended forward.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	tripsJob      = "trips"
	tripsMatch    = `{job="trips"}`
	fixtureMatch  = `{job="trickster_fixtures"}`
	graphiteMatch = `{__name__=~"vmgraphite[.].+"}`

	// trips history exists this far back only once it has been imported, as
	// devorigin's live samples cannot reach it in a freshly started environment
	historyProbeAge = 7 * 24 * time.Hour
	// covers live trips samples scraped before the history probe could see them
	liveLookback = "8d"
	// covers the retention period, for finding the newest fixture sample
	fixtureLookback = "32d"

	// the smallest per-query latency offset VictoriaMetrics accepts, so freshly
	// imported samples near the current time are counted
	latencyOffset = "1ms"

	maxErrorBody = 1024
)

type seeder struct {
	baseURL  string
	omPath   string
	instance string
	timeout  time.Duration
	poll     time.Duration
	now      func() time.Time
	client   *http.Client
	log      io.Writer
}

// importStats summarizes an import for validation; timestamps are in seconds.
type importStats struct {
	samples      int64
	minTS, maxTS int64
	// only samples before a nonzero cutoff are counted for validation, since
	// live scrapes add samples to the same series from then on
	cutoff      int64
	counted     int64
	lastCounted int64
}

// fixtureGroup is one fixture import, found again by its canary series.
type fixtureGroup struct {
	name, match, canary string
	series              []series
}

// queryResponse is the part of an instant query response the seeder reads.
type queryResponse struct {
	Status string    `json:"status"`
	Error  string    `json:"error"`
	Data   queryData `json:"data"`
}

type queryData struct {
	Result []querySample `json:"result"`
}

type querySample struct {
	Value [2]any `json:"value"`
}

type countCheck struct {
	name, expr string
	at, want   int64
}

func main() {
	s := &seeder{
		baseURL:  strings.TrimRight(envOr("VM_URL", "http://victoriametrics:8428"), "/"), //nolint:revive // developer-environment default
		omPath:   envOr("VM_SEED_OM", "/om/trips.om"),
		instance: envOr("VM_SEED_INSTANCE", "devorigin:8482"),
		timeout:  envDuration("VM_SEED_TIMEOUT", 5*time.Minute),
		poll:     2 * time.Second,
		now:      time.Now,
		client:   &http.Client{},
		log:      os.Stdout,
	}
	if err := s.run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "victoriametrics seed:", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil && d > 0 {
		return d
	}
	return fallback
}

func (s *seeder) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.log, format+"\n", args...)
}

func (s *seeder) run() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	// the backfill file is large and only this run reads it
	defer os.Remove(s.omPath)
	if err := s.waitHealthy(ctx); err != nil {
		return err
	}
	checks, err := s.seedTrips(ctx)
	if err != nil {
		return err
	}
	anchorSec := s.now().Truncate(time.Minute).Unix()
	prom, graphite := buildFixtures(anchorSec)
	groups := []fixtureGroup{
		{"fixtures", fixtureMatch, fixtureCanary, prom},
		{"graphite fixtures", graphiteMatch, `{__name__="` + graphiteCanary + `"}`, graphite},
	}
	for _, g := range groups {
		c, ok, err := s.seedFixtures(ctx, g, anchorSec)
		if err != nil {
			return err
		}
		if ok {
			checks = append(checks, c)
		}
	}
	// backfilled samples can be older than the rollup result cache's horizon
	if err := s.get(ctx, "/internal/resetRollupResultCache", nil); err != nil {
		return err
	}
	if err := s.validate(ctx, checks); err != nil {
		return err
	}
	if err := s.validateGraphite(ctx); err != nil {
		return err
	}
	s.logf("victoriametrics seed complete")
	return nil
}

// seedTrips imports the trips history unless the storage already has it.
func (s *seeder) seedTrips(ctx context.Context) ([]countCheck, error) {
	probeSec := s.now().Add(-historyProbeAge).Unix()
	if n, ok, err := s.query(ctx, "count(count_over_time("+tripsMatch+"[1h]))", probeSec); err != nil {
		return nil, err
	} else if ok && n > 0 {
		s.logf("trips history is already present; keeping it (make developer-seed-data reseeds from empty storage)")
		return nil, nil
	}
	liveFrom, _, err := s.query(ctx, "min(tfirst_over_time("+tripsMatch+"["+liveLookback+"]))", s.now().Unix())
	if err != nil {
		return nil, err
	}
	st, err := s.importOpenMetrics(ctx, int64(liveFrom))
	if err != nil {
		return nil, err
	}
	s.logf("imported %d trips samples from %s to %s", st.samples, fmtUnix(st.minTS), fmtUnix(st.maxTS))
	if c, ok := st.check("trips", tripsMatch); ok {
		return []countCheck{c}, nil
	}
	s.logf("live trips samples predate the history, so its count is not verified")
	return nil, nil
}

// seedFixtures imports the group's samples newer than its canary's newest one.
func (s *seeder) seedFixtures(ctx context.Context, g fixtureGroup, anchor int64) (countCheck, bool, error) {
	last, _, err := s.query(ctx, "max(tlast_over_time("+g.canary+"["+fixtureLookback+"]))", s.now().Unix())
	if err != nil {
		return countCheck{}, false, err
	}
	fresh := after(g.series, int64(last))
	st := statsOf(fresh)
	if st.samples == 0 {
		s.logf("%s are current through %s", g.name, fmtUnix(anchor))
		return countCheck{}, false, nil
	}
	var body bytes.Buffer
	if err := writeJSONLines(&body, fresh); err != nil {
		return countCheck{}, false, err
	}
	if err := s.post(ctx, "/api/v1/import", &body, ""); err != nil {
		return countCheck{}, false, err
	}
	s.logf("imported %d %s samples from %s to %s", st.samples, g.name, fmtUnix(st.minTS), fmtUnix(st.maxTS))
	c, ok := st.check(g.name, g.match)
	return c, ok, nil
}

func fmtUnix(sec int64) string {
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// check counts the counted samples, minTS through lastCounted, at lastCounted.
// Other samples of the matched series must fall outside that range.
func (st importStats) check(name, match string) (countCheck, bool) {
	if st.counted == 0 {
		return countCheck{}, false
	}
	return countCheck{
		name: name,
		expr: fmt.Sprintf("sum(count_over_time(%s[%ds]))", match, st.lastCounted-st.minTS+1),
		at:   st.lastCounted,
		want: st.counted,
	}, true
}

func (s *seeder) waitHealthy(ctx context.Context) error {
	var last error
	for {
		if last = s.get(ctx, "/health", nil); last == nil {
			return nil
		}
		if err := s.sleep(ctx); err != nil {
			return fmt.Errorf("victoriametrics is not healthy: %w", last)
		}
	}
}

func (s *seeder) sleep(ctx context.Context) error {
	t := time.NewTimer(s.poll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// importOpenMetrics streams the backfill file gzip-compressed to VictoriaMetrics,
// labeled as the live trips scrape target is, so history and live data join up.
// Samples from liveFrom on are not counted, as live scrapes share their range.
func (s *seeder) importOpenMetrics(ctx context.Context, liveFrom int64) (importStats, error) {
	f, err := os.Open(s.omPath)
	if err != nil {
		return importStats{}, fmt.Errorf("missing trips backfill: %w", err)
	}
	defer f.Close()
	pr, pw := io.Pipe()
	st := importStats{cutoff: liveFrom}
	done := make(chan error, 1)
	go func() {
		err := compressOpenMetrics(f, pw, &st)
		_ = pw.CloseWithError(err)
		done <- err
	}()
	q := url.Values{"extra_label": {"job=" + tripsJob, "instance=" + s.instance}}
	err = s.post(ctx, "/api/v1/import/prometheus?"+q.Encode(), pr, "gzip")
	// unblocks the compressor if the request ended before reading everything
	_ = pr.Close()
	if cerr := <-done; cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
		return st, cerr
	}
	if err != nil {
		return st, err
	}
	if st.samples == 0 {
		return st, errors.New("the trips backfill has no samples")
	}
	return st, nil
}

// compressOpenMetrics copies OpenMetrics text to w as gzip, recording the
// sample count and time bounds; it rejects files missing the closing # EOF.
func compressOpenMetrics(r io.Reader, w io.Writer, st *importStats) error {
	zw, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	sawEOF := false
	for {
		line, rerr := br.ReadSlice('\n')
		if errors.Is(rerr, bufio.ErrBufferFull) {
			return errors.New("an OpenMetrics line exceeds 64 KiB")
		}
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			sawEOF = string(trimmed) == "# EOF"
			if err := st.observe(trimmed); err != nil {
				return err
			}
			if _, err := zw.Write(line); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if !sawEOF {
		return errors.New("the trips backfill is truncated (no # EOF)")
	}
	return zw.Close()
}

// observe counts a sample line; devorigin writes integer second timestamps.
func (st *importStats) observe(line []byte) error {
	if len(line) == 0 || line[0] == '#' {
		return nil
	}
	_, after, ok := bytes.CutLast(line, []byte{' '})
	if !ok {
		return fmt.Errorf("malformed OpenMetrics sample %q", line)
	}
	ts, err := strconv.ParseInt(string(after), 10, 64)
	if err != nil {
		return fmt.Errorf("OpenMetrics sample without an integer timestamp: %q", line)
	}
	st.add(ts)
	return nil
}

func (st *importStats) add(ts int64) {
	if st.samples == 0 || ts < st.minTS {
		st.minTS = ts
	}
	if st.samples == 0 || ts > st.maxTS {
		st.maxTS = ts
	}
	st.samples++
	if st.cutoff == 0 || ts < st.cutoff {
		st.counted++
		st.lastCounted = max(st.lastCounted, ts)
	}
}

func statsOf(ss []series) importStats {
	var st importStats
	for _, s := range ss {
		for _, ts := range s.ts {
			st.add(ts)
		}
	}
	return st
}

// validate polls until each count matches, since imported samples become
// searchable shortly after the import request returns.
func (s *seeder) validate(ctx context.Context, checks []countCheck) error {
	for _, c := range checks {
		var last error
		for {
			got, _, err := s.query(ctx, c.expr, c.at)
			if err == nil && int64(got) == c.want {
				s.logf("verified %d %s samples", c.want, c.name)
				break
			}
			// a query cut short by the deadline would hide the last real outcome
			switch {
			case err == nil:
				last = fmt.Errorf("found %d samples, want %d", int64(got), c.want)
			case last == nil || ctx.Err() == nil:
				last = err
			}
			if s.sleep(ctx) != nil {
				return fmt.Errorf("verifying %s: %w", c.name, last)
			}
		}
	}
	return nil
}

// validateGraphite checks that the Graphite APIs discover the fixture names and tags.
func (s *seeder) validateGraphite(ctx context.Context) error {
	var nodes []struct {
		Text string `json:"text"`
	}
	if err := s.get(ctx, "/metrics/find?query="+graphiteRoot+".*", &nodes); err != nil {
		return err
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.Text)
	}
	slices.Sort(got)
	if want := graphiteBranches(); !slices.Equal(got, want) {
		return fmt.Errorf("graphite find %s.* returned %v, want %v", graphiteRoot, got, want)
	}
	// an unfiltered /tags lists nothing for labels imported this way, so the
	// tag names are discovered through an expression, as Grafana's editor does
	var tags []string
	if err := s.get(ctx, "/tags/autoComplete/tags?"+url.Values{"expr": {"name=" + graphiteTagged}}.Encode(), &tags); err != nil {
		return err
	}
	for _, want := range graphiteTagNames {
		if !slices.Contains(tags, want) {
			return fmt.Errorf("graphite tags of %s are %v, missing %q", graphiteTagged, tags, want)
		}
	}
	s.logf("verified graphite discovery of %v and tags %v", got, graphiteTagNames)
	return nil
}

func (s *seeder) query(ctx context.Context, expr string, at int64) (float64, bool, error) {
	q := url.Values{
		"query":          {expr},
		"time":           {strconv.FormatInt(at, 10)},
		"nocache":        {"1"},
		"latency_offset": {latencyOffset},
	}
	var resp queryResponse
	if err := s.get(ctx, "/api/v1/query?"+q.Encode(), &resp); err != nil {
		return 0, false, err
	}
	if resp.Status != "success" {
		return 0, false, fmt.Errorf("query %s: %s", expr, resp.Error)
	}
	if len(resp.Data.Result) == 0 {
		return 0, false, nil
	}
	raw, _ := resp.Data.Result[0].Value[1].(string)
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("query %s: bad value %q", expr, raw)
	}
	return v, true, nil
}

// get fetches path and, when out is non-nil, decodes its JSON response into it.
func (s *seeder) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := s.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

// post sends body to path with an optional Content-Encoding.
func (s *seeder) post(ctx context.Context, path string, body io.Reader, encoding string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+path, body)
	if err != nil {
		return err
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := s.do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// do sends req and turns any non-2xx response into an error.
func (s *seeder) do(req *http.Request) (*http.Response, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}
