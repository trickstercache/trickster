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

package parsing

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Friday 2026-08-21 20:30:45 UTC
var testNow = time.Date(2026, 8, 21, 20, 30, 45, 0, time.UTC)

func utc(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
}

func TestParseATTime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		in   string
		loc  *time.Location
		want time.Time
		err  bool
	}{
		// references
		{"now", nil, testNow, false},
		{"", nil, testNow, false},
		{" NOW ", nil, testNow, false},
		{"midnight", nil, utc(2026, 8, 21, 0, 0), false},
		{"noon", nil, utc(2026, 8, 21, 12, 0), false},
		{"teatime", nil, utc(2026, 8, 21, 16, 0), false},
		{"today", nil, utc(2026, 8, 21, 0, 0), false},
		{"yesterday", nil, utc(2026, 8, 20, 0, 0), false},
		{"tomorrow", nil, utc(2026, 8, 22, 0, 0), false},
		{"midnight yesterday", nil, utc(2026, 8, 20, 0, 0), false},
		{"midnight_tomorrow", nil, utc(2026, 8, 22, 0, 0), false},
		{"noon today", nil, utc(2026, 8, 21, 12, 0), false},
		// relative offsets
		{"-7d", nil, testNow.Add(-7 * 24 * time.Hour), false},
		{"-90min", nil, testNow.Add(-90 * time.Minute), false},
		{"-1mon", nil, testNow.Add(-30 * 24 * time.Hour), false},
		{"-1month", nil, testNow.Add(-30 * 24 * time.Hour), false},
		{"-2y", nil, testNow.Add(-730 * 24 * time.Hour), false},
		{"-1w", nil, testNow.Add(-7 * 24 * time.Hour), false},
		{"-30s", nil, testNow.Add(-30 * time.Second), false},
		{"-1h30min", nil, testNow.Add(-90 * time.Minute), false},
		{"+1h", nil, testNow.Add(time.Hour), false},
		{"-0d", nil, testNow, false},
		{"- 7d", nil, testNow.Add(-7 * 24 * time.Hour), false},
		{"now-1h", nil, testNow.Add(-time.Hour), false},
		{"midnight-1d", nil, utc(2026, 8, 20, 0, 0), false},
		{"today+1h", nil, utc(2026, 8, 21, 1, 0), false},
		{"-5m", nil, time.Time{}, true}, // graphite: Invalid offset unit 'm'
		{"-5", nil, time.Time{}, true},
		{"-d", nil, time.Time{}, true},
		{"-1x", nil, time.Time{}, true},
		{"-99999999999999999999d", nil, time.Time{}, true},
		{"-9999999999999y", nil, time.Time{}, true},
		// epoch and date forms
		{"1787343600", nil, time.Unix(1787343600, 0).UTC(), false},
		{"20260821", nil, utc(2026, 8, 21, 0, 0), false},
		{"20261301", nil, time.Unix(20261301, 0).UTC(), false}, // not a date: epoch
		{"08/21/26", nil, utc(2026, 8, 21, 0, 0), false},
		{"08/21/2026", nil, utc(2026, 8, 21, 0, 0), false},
		{"12/31/99", nil, utc(1999, 12, 31, 0, 0), false},
		{"02/30/26", nil, time.Time{}, true},
		{"13/01/26", nil, time.Time{}, true},
		{"a/b/c", nil, time.Time{}, true},
		{"14:30_20260821", nil, utc(2026, 8, 21, 14, 30), false},
		{"14:30 20260821", nil, utc(2026, 8, 21, 14, 30), false},
		{"9:00am", nil, utc(2026, 8, 21, 9, 0), false},
		{"9:30pm", nil, utc(2026, 8, 21, 21, 30), false},
		{"9pm", nil, utc(2026, 8, 21, 21, 0), false},
		{"11am", nil, utc(2026, 8, 21, 11, 0), false},
		{"12pm", nil, utc(2026, 8, 21, 0, 0), false}, // (12+12)%24, as in graphite
		{"6:00pm yesterday", nil, utc(2026, 8, 20, 18, 0), false},
		{"25:00", nil, time.Time{}, true},
		{"jan15", nil, utc(2026, 1, 15, 0, 0), false},
		{"aug 5", nil, utc(2026, 8, 5, 0, 0), false},
		{"january1", nil, utc(2026, 1, 1, 0, 0), false},
		{"feb30", nil, time.Time{}, true},
		{"jan", nil, time.Time{}, true},
		{"monday", nil, utc(2026, 8, 17, 0, 0), false},
		{"fri", nil, utc(2026, 8, 21, 0, 0), false},
		{"sat", nil, utc(2026, 8, 15, 0, 0), false},
		{"sunday", nil, utc(2026, 8, 16, 0, 0), false},
		{"2026-01-01", nil, time.Time{}, true},
		{"foo", nil, time.Time{}, true},
		{"now+", nil, testNow, false},
		// time zones
		{"midnight", ny, time.Date(2026, 8, 21, 0, 0, 0, 0, ny), false},
		{"08/21/26", ny, time.Date(2026, 8, 21, 0, 0, 0, 0, ny), false},
		{"-1h", ny, testNow.Add(-time.Hour), false},
		{"1787343600", ny, time.Unix(1787343600, 0), false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseATTime(tc.in, tc.loc, testNow)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestParseTimeOffset(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", 0, false},
		{"1h", time.Hour, false},
		{"+1h", time.Hour, false},
		{"-1h", -time.Hour, false},
		{"10min", 10 * time.Minute, false},
		{"10minutes", 10 * time.Minute, false},
		{"1d12h", 36 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"1mon", 30 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"1seconds", time.Second, false},
		{"x1h", 0, true},
		{"1m", 0, true},
		{"1", 0, true},
		{"h", 0, true},
		{"1h5", 0, true},
		{"9223372036854775808s", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseTimeOffset(tc.in)
			if (err != nil) != tc.err {
				t.Fatalf("err=%v want error=%t", err, tc.err)
			}
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestParseTimeRange(t *testing.T) {
	now := testNow.Add(500 * time.Millisecond)
	sec := testNow
	tests := []struct {
		from, until string
		start, end  time.Time
		err         error
	}{
		{"", "", sec.Add(-24 * time.Hour), sec, nil},
		{"-1h", "now", sec.Add(-time.Hour), sec, nil},
		{"now", "-1h", sec.Add(-time.Hour), sec, nil}, // ordered
		{"1787343600", "1787347200", time.Unix(1787343600, 0).UTC(), time.Unix(1787347200, 0).UTC(), nil},
		{"now", "now", time.Time{}, time.Time{}, ErrEmptyTimeRange},
		{"-1h", "-1h", time.Time{}, time.Time{}, ErrEmptyTimeRange},
		{"bad", "", time.Time{}, time.Time{}, errBadOffset},
		{"", "bad", time.Time{}, time.Time{}, errBadOffset},
	}
	for _, tc := range tests {
		t.Run(tc.from+"/"+tc.until, func(t *testing.T) {
			ext, err := ParseTimeRange(tc.from, tc.until, nil, now)
			if tc.err != nil {
				if err == nil || (tc.err != errBadOffset && !errors.Is(err, tc.err)) {
					t.Fatalf("expected %v, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !ext.Start.Equal(tc.start) || !ext.End.Equal(tc.end) {
				t.Errorf("got [%v, %v] want [%v, %v]", ext.Start, ext.End, tc.start, tc.end)
			}
		})
	}
}

func FuzzParseATTime(f *testing.F) {
	for _, s := range []string{"now", "-7d", "midnight yesterday", "08/21/26", "14:30_20260821",
		"9:30pm", "jan15", "monday", "1787343600", "20260821", "-1h30min", "today+1h", "", "-", "+", ":"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// must never panic; either a time or an error
		_, _ = ParseATTime(s, time.UTC, testNow)
		_, _ = ParseTimeOffset(s)
	})
}

func TestParseATTimeAgainstGraphiteWeb(t *testing.T) {
	// cross-checks the port against a live graphite-web when GRAPHITE_WEB_URL
	// is set; comparisons are made at the serving rung's step
	base := os.Getenv("GRAPHITE_WEB_URL")
	if base == "" {
		t.Skip("GRAPHITE_WEB_URL not set")
	}
	now := time.Now().Truncate(time.Second)
	ny, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		from string
		tz   string
		loc  *time.Location
	}{
		{"-1h", "", time.UTC}, {"-90min", "", time.UTC}, {"midnight", "", time.UTC},
		{"noon", "", time.UTC}, {"teatime", "", time.UTC}, {"yesterday", "", time.UTC},
		{"midnight yesterday", "", time.UTC}, {"today-1h", "", time.UTC},
		{"monday", "", time.UTC}, {"9:30pm", "", time.UTC}, {"6:00pm yesterday", "", time.UTC},
		{"midnight", "America/New_York", ny}, {"08/21/26", "America/New_York", ny},
		{now.Add(-2 * time.Hour).Format("20060102"), "", time.UTC},
	}
	for _, tc := range cases {
		want, err := ParseATTime(tc.from, tc.loc, now)
		if err != nil {
			t.Fatalf("%s: %v", tc.from, err)
		}
		if !want.Before(now) {
			continue
		}
		q := url.Values{"target": {"dev.fast.cpu.host01.percent"}, "from": {tc.from},
			"until": {"now"}, "now": {strconv.FormatInt(now.Unix(), 10)}, "format": {"raw"}}
		if tc.tz != "" {
			q.Set("tz", tc.tz)
		}
		resp, err := http.Get(base + "/render?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		head, _, _ := strings.Cut(strings.TrimSpace(string(body)), "|")
		parts := strings.Split(head, ",")
		if len(parts) < 4 {
			t.Fatalf("%s: unexpected raw response %q", tc.from, body)
		}
		start, _ := strconv.ParseInt(parts[1], 10, 64)
		step, _ := strconv.ParseInt(parts[3], 10, 64)
		// whisper aligns the header's start to the serving rung's step:
		// (from - from%step) + step
		w := want.Unix()
		expected := w - w%step + step
		if start != expected {
			t.Errorf("%s (tz=%s): graphite start %d, port predicts %d (from=%d): %s",
				tc.from, tc.tz, start, expected, w, fmt.Sprint(want))
		}
	}
}
