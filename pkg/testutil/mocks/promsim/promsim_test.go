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

package promsim

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	testQuery             = `myQuery{other_label=5,test}`
	expectedRangeOutput   = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"other_label":"5","test":"","series_id":"0"},"values":[[0,"64"],[1800,"9"],[3600,"87"]]}]}}`
	expectedInstantOutput = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"other_label":"5","test":"","series_id":"0"},"value":[1800,"9"]}]}}`
)

func TestGetTimeSeriesData(t *testing.T) {
	out, code := GetTimeSeriesData(testQuery, time.Unix(0, 0), time.Unix(3600, 0), 1800*time.Second)
	if code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, code)
	}
	if out != expectedRangeOutput {
		t.Errorf("expected %s got %s", expectedRangeOutput, out)
	}
}

func TestGetTimeSeriesDataAlignsToEnd(t *testing.T) {
	out, _ := GetTimeSeriesData("up", time.Unix(5, 0), time.Unix(30, 0), 15*time.Second)
	if !strings.Contains(out, `"values":[[15,`) || !strings.Contains(out, `[30,`) {
		t.Errorf("expected points at 15 and 30, got %s", out)
	}
}

func TestGetTimeSeriesDataSubrangesMatchFullRange(t *testing.T) {
	// the delta proxy cache tests rely on merged partial ranges equaling a full range
	const step = 15 * time.Second
	start, mid, end := time.Unix(1_700_000_000, 0), time.Unix(1_700_003_600, 0), time.Unix(1_700_007_200, 0)
	full, _ := GetTimeSeriesData(testQuery, start, end, step)
	left, _ := GetTimeSeriesData(testQuery, start, mid, step)
	right, _ := GetTimeSeriesData(testQuery, mid.Add(step), end, step)
	values := func(s string) string {
		_, v, _ := strings.Cut(s, `"values":[`)
		return strings.TrimSuffix(v, "]}]}}")
	}
	if got := values(left) + "," + values(right); got != values(full) {
		t.Errorf("merged subranges do not match the full range:\n%s\n%s", got, values(full))
	}
	again, _ := GetTimeSeriesData(testQuery, start, end, step)
	if again != full {
		t.Error("expected repeatable output")
	}
}

func TestGetInstantData(t *testing.T) {
	out, code := GetInstantData(testQuery, time.Unix(1800, 0))
	if code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, code)
	}
	if out != expectedInstantOutput {
		t.Errorf("expected %s got %s", expectedInstantOutput, out)
	}
	out, _ = GetInstantData("up", time.Time{})
	if !strings.Contains(out, `"resultType":"vector"`) {
		t.Errorf("expected a vector for the current time, got %s", out)
	}
}

func TestInvalidResponseBody(t *testing.T) {
	const q = "myQuery{invalid_response_body=1}"
	if out, _ := GetTimeSeriesData(q, time.Unix(0, 0), time.Unix(3600, 0), 1800*time.Second); out != invalidBody {
		t.Errorf("expected %s got %s", invalidBody, out)
	}
	if out, _ := GetInstantData(q, time.Unix(0, 0)); out != invalidBody {
		t.Errorf("expected %s got %s", invalidBody, out)
	}
}

func TestParseModifiers(t *testing.T) {
	m := parseModifiers(`myQuery{other_label=a5,status_code=502,test,quoted="x"}`)
	const expected = `"other_label":"a5","status_code":"502","test":"","quoted":"x","series_id":"0"`
	if m.labels != expected {
		t.Errorf("expected %s got %s", expected, m.labels)
	}
	if m.statusCode != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, m.statusCode)
	}
	if m := parseModifiers("up{}"); m.labels != `"series_id":"0"` || m.statusCode != http.StatusOK {
		t.Errorf("unexpected modifiers for an empty selector: %+v", m)
	}
}

func TestGetTimeSeriesDataRejectsBadRanges(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	for name, c := range map[string]struct {
		end  time.Time
		step time.Duration
	}{
		"zero step":         {start.Add(time.Hour), 0},
		"negative step":     {start.Add(time.Hour), -time.Second},
		"end before start":  {start.Add(-time.Hour), time.Second},
		"too many points":   {start.Add((MaxResolution + 1) * time.Second), time.Second},
		"saturated range":   {start.Add(1 << 62), time.Nanosecond},
		"reversed saturate": {start.Add(-1 << 62), time.Nanosecond},
	} {
		out, code := GetTimeSeriesData(testQuery, start, c.end, c.step)
		if code != http.StatusBadRequest || !strings.Contains(out, `"errorType":"bad_data"`) {
			t.Errorf("%s: expected a 400 bad_data response, got %d %s", name, code, out)
		}
	}
	out, code := GetTimeSeriesData(testQuery, start, start.Add(MaxResolution*time.Second), time.Second)
	if code != http.StatusOK || strings.Count(out, "],[")+1 != MaxResolution+1 {
		t.Errorf("expected %d points at the limit, got %d", MaxResolution+1, code)
	}
}

func TestLabelsAreValidJSON(t *testing.T) {
	q := "up{a\\b=c\\d,we\"ird=v\nx,bare\\,status_code=0,x=1}"
	out, code := GetInstantData(q, time.Unix(0, 0))
	if code != http.StatusOK {
		t.Errorf("expected an out-of-range status_code to be ignored, got %d", code)
	}
	var doc struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("invalid JSON %s: %v", out, err)
	}
	m := doc.Data.Result[0].Metric
	if m[`a\b`] != `c\d` || m[`we"ird`] != "v\nx" || m[`bare\`] != "" || m["status_code"] != "0" {
		t.Errorf("unexpected labels %v", m)
	}
	for _, sc := range []string{"99", "600", "1000", "-1"} {
		if _, code := GetInstantData("up{status_code="+sc+"}", time.Unix(0, 0)); code != http.StatusOK {
			t.Errorf("status_code=%s: expected 200 got %d", sc, code)
		}
	}
}
