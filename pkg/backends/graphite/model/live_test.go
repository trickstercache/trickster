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

package model

import (
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestAgainstGraphiteWeb(t *testing.T) {
	// fetches JSON from a live graphite-web (GRAPHITE_WEB_URL), models it, and checks
	// every client format Trickster renders is byte-identical to the origin's
	base := os.Getenv("GRAPHITE_WEB_URL")
	if base == "" {
		t.Skip("GRAPHITE_WEB_URL not set")
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	get := func(q url.Values) string {
		t.Helper()
		q.Set("now", now)
		resp, err := http.Get(base + "/render?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	cases := []struct {
		name    string
		targets []string
		from    string
		step    time.Duration
	}{
		{"fast 10s", []string{"dev.fast.cpu.host01.percent"}, "-5min", 10 * time.Second},
		{"fast 60s rung", []string{"dev.fast.requests.api.count"}, "-12h", time.Minute},
		{"multi-target", []string{"dev.fast.cpu.host01.percent", "dev.coarse.users.active"}, "-30min", 0},
		{"wildcard + function", []string{"aliasByNode(dev.fast.requests.*.count, 3)"}, "-10min", 10 * time.Second},
		{"nulls", []string{"dev.fast.latency.api.p99"}, "-5min", 10 * time.Second},
		{"consolidateBy", []string{"consolidateBy(dev.fast.cpu.host01.percent,'max')"}, "-20min", 10 * time.Second},
		{"storage integral floats", []string{"dev.coarse.storage.bucket-a.bytes"}, "-2h", 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := url.Values{"from": {tc.from}, "until": {"now"}}
			for _, tg := range tc.targets {
				params.Add("target", tg)
			}
			q := url.Values{}
			maps.Copy(q, params)
			q.Set("format", "json")
			upstream := get(q)
			q0 := &timeseries.TimeRangeQuery{Step: tc.step}
			ts, err := UnmarshalTimeseries([]byte(upstream), q0)
			if len(tc.targets) > 1 {
				// two targets on different ladders: graphite-web returns
				// each at its native step, which one query cannot model
				if !errors.Is(err, ErrStepMismatch) {
					t.Fatalf("expected ErrStepMismatch for mixed steps, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			check := func(label string, extra url.Values, ro RenderOptions) {
				t.Helper()
				q := url.Values{}
				maps.Copy(q, params)
				maps.Copy(q, extra)
				want := get(q)
				got, err := MarshalTimeseries(ts, &timeseries.RequestOptions{ProviderRequest: ro}, 200)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != want {
					t.Errorf("%s differs\n got: %.300s\nwant: %.300s", label, got, want)
				}
			}
			check("json", url.Values{"format": {"json"}}, RenderOptions{})
			check("raw", url.Values{"format": {"raw"}}, RenderOptions{Format: FormatRaw})
			check("csv", url.Values{"format": {"csv"}}, RenderOptions{Format: FormatCSV})
			ny, _ := time.LoadLocation("America/New_York")
			check("csv tz", url.Values{"format": {"csv"}, "tz": {"America/New_York"}}, RenderOptions{Format: FormatCSV, Location: ny})
			check("pretty", url.Values{"format": {"json"}, "pretty": {"1"}}, RenderOptions{Pretty: true})
			check("jsonp", url.Values{"format": {"json"}, "jsonp": {"cb"}}, RenderOptions{JSONP: "cb"})
			check("noNullPoints", url.Values{"format": {"json"}, "noNullPoints": {"true"}}, RenderOptions{NoNullPoints: true})
			for _, mdp := range []int{1, 2, 3, 7, 11, 50, 1000} {
				check("maxDataPoints="+strconv.Itoa(mdp), url.Values{"format": {"json"}, "maxDataPoints": {strconv.Itoa(mdp)}},
					RenderOptions{MaxDataPoints: mdp})
			}
			// msgpack's pathExpression for a function target is graphite-web's
			// normalized name, which Trickster does not reproduce
			if len(tc.targets) == 1 && !strings.Contains(tc.targets[0], "(") {
				check("msgpack", url.Values{"format": {"msgpack"}}, RenderOptions{Format: FormatMsgPack, PathExpressions: tc.targets})
			}
		})
	}
}
