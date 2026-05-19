/*
 * Copyright 2018 The Trickster Authors
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

// Package metricsutil provides Prometheus-text-format scraping helpers for
// integration tests. Tests can take a before/after snapshot of Trickster's
// /metrics endpoint and assert delta on a specific counter+label set.
//
// The parser handles the subset of the Prometheus text exposition format
// that Trickster produces: counters, gauges, summaries, and histograms.
// Comment / HELP / TYPE lines are skipped. Histograms and summaries are
// expanded into their _count, _sum, and _bucket child series so the
// resulting map is uniform: name+labels -> float64.
package metricsutil

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scrapeTransport is shared so connections can be reused; rapid per-call
// dial cycles otherwise exhaust macOS ephemeral ports under matrix-style
// fanouts. Held as the bare transport so callers can flush idle conns
// between scrapes against distinct test servers.
var scrapeTransport = &http.Transport{
	MaxIdleConns:        16,
	MaxIdleConnsPerHost: 4,
	IdleConnTimeout:     30 * time.Second,
	DisableKeepAlives:   false,
}

var scrapeClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: scrapeTransport,
}

// Scrape fetches Trickster's /metrics endpoint and returns a parsed map
// keyed by canonical name+label form, value is the sample value. Retries
// transient dial errors a few times to absorb ephemeral-port exhaustion
// during dense matrix-style tests.
func Scrape(t testing.TB, metricsPort int) map[string]float64 {
	t.Helper()
	return ScrapeURL(t, fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort), scrapeClient)
}

// ScrapeURL is like Scrape but takes an explicit URL and HTTP client.
// Useful in unit tests that want to reuse an httptest.Server's own
// keepalive-tracking client to avoid macOS ephemeral-port races.
func ScrapeURL(t testing.TB, url string, cli *http.Client) map[string]float64 {
	t.Helper()
	if cli == nil {
		cli = scrapeClient
	}
	var lastErr error
	for attempt := range 3 {
		resp, err := cli.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(25*(attempt+1)) * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("scrape %s returned %d", url, resp.StatusCode)
			time.Sleep(time.Duration(25*(attempt+1)) * time.Millisecond)
			continue
		}
		out, perr := Parse(resp.Body)
		resp.Body.Close()
		require.NoError(t, perr, "parse text exposition from %s", url)
		return out
	}
	require.NoErrorf(t, lastErr, "scrape %s after retries", url)
	return nil
}

// Parse reads Prometheus text-format exposition from r and returns the
// canonical name+label -> value map. Lines beginning with `#` are skipped.
// NaN values are returned as the float NaN; tests that want to ignore them
// should filter on math.IsNaN.
func Parse(r io.Reader) (map[string]float64, error) {
	out := make(map[string]float64, 256)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out[Key(name, labels)] = value
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseLine consumes a single sample line: `name{l1="v1",l2="v2"} 3.14 [ts]`.
// The optional trailing timestamp (Prometheus exposition allows it) is
// ignored. Label values may contain escaped quotes (`\"`), backslashes
// (`\\`), and newlines (`\n`); the parser unescapes them.
func parseLine(line string) (string, map[string]string, float64, error) {
	// Split name [+ optional `{labels}`] from the trailing value.
	var headEnd int
	if brace := strings.IndexByte(line, '{'); brace >= 0 {
		// Find the matching closing brace, honoring escaped quotes inside
		// label values.
		close := findClosingBrace(line, brace)
		if close < 0 {
			return "", nil, 0, fmt.Errorf("unterminated label set: %q", line)
		}
		headEnd = close + 1
	} else if sp := strings.IndexByte(line, ' '); sp > 0 {
		headEnd = sp
	} else {
		return "", nil, 0, fmt.Errorf("malformed line: %q", line)
	}

	head := line[:headEnd]
	tail := strings.TrimSpace(line[headEnd:])

	var name string
	var labels map[string]string
	if brace := strings.IndexByte(head, '{'); brace >= 0 {
		name = strings.TrimSpace(head[:brace])
		lset, err := parseLabels(head[brace+1 : len(head)-1])
		if err != nil {
			return "", nil, 0, err
		}
		labels = lset
	} else {
		name = head
	}

	// Strip trailing timestamp if present (a second space-separated number).
	if sp := strings.IndexByte(tail, ' '); sp >= 0 {
		tail = tail[:sp]
	}
	v, err := strconv.ParseFloat(tail, 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("parse value %q: %w", tail, err)
	}
	return name, labels, v, nil
}

func findClosingBrace(s string, open int) int {
	// Scan past escaped quotes inside label values. The grammar guarantees
	// braces only appear at the boundary, but values may contain `}`
	// characters, so we have to track quoting state.
	inQuotes := false
	for i := open + 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++ // skip escaped char
		case c == '"':
			inQuotes = !inQuotes
		case c == '}' && !inQuotes:
			return i
		}
	}
	return -1
}

func parseLabels(s string) (map[string]string, error) {
	out := make(map[string]string)
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, fmt.Errorf("label without =: %q", s)
		}
		key := strings.TrimSpace(s[:eq])
		rest := s[eq+1:]
		if len(rest) == 0 || rest[0] != '"' {
			return nil, fmt.Errorf("label value not quoted at %q", rest)
		}
		// Find closing quote, honoring escapes.
		end := -1
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\\' && i+1 < len(rest) {
				i++
				continue
			}
			if rest[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("unterminated label value: %q", rest)
		}
		val := unescapeLabel(rest[1:end])
		out[key] = val
		s = rest[end+1:]
		// Skip optional comma + whitespace before next label.
		s = strings.TrimLeft(s, ", \t")
	}
	return out, nil
}

func unescapeLabel(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Key returns the canonical map key for a metric name + label set. Exposed
// so callers can build the key without going through Scrape (useful when
// you want to assert "metric absent" by checking _, ok := snap[Key(...)]).
func Key(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.Grow(len(name) + 2 + len(labels)*16)
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(labels[k])
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// RequireDelta asserts that (after - before) for the named metric with the
// given labels equals expected. A missing key counts as zero; passing
// expected=0 with both snapshots absent therefore passes (useful for
// "did not increment" assertions).
func RequireDelta(t testing.TB, before, after map[string]float64, name string, labels map[string]string, expected float64) {
	t.Helper()
	k := Key(name, labels)
	delta := after[k] - before[k]
	require.InDeltaf(t, expected, delta, 1e-9,
		"metric %s delta=%v want=%v (before=%v after=%v)",
		k, delta, expected, before[k], after[k])
}

// RequireDeltaGreater asserts that (after - before) for the named metric with
// the given labels is strictly greater than minDelta. Useful when the exact
// count is non-deterministic (e.g. flapping cells) but you want to confirm
// the counter moved.
func RequireDeltaGreater(t testing.TB, before, after map[string]float64, name string, labels map[string]string, minDelta float64) {
	t.Helper()
	k := Key(name, labels)
	delta := after[k] - before[k]
	require.Greaterf(t, delta, minDelta,
		"metric %s delta=%v want > %v (before=%v after=%v)",
		k, delta, minDelta, before[k], after[k])
}
