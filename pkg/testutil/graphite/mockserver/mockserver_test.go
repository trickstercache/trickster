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

package mockserver

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, s *Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(s.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStub(t *testing.T) {
	s := New()
	defer s.Close()
	now := time.Unix(1_787_350_000, 0)
	s.Now = func() time.Time { return now }
	s.Add("a.b.c", "10s:6h,60s:7d")
	s.Add("a.b.d", "10s:6h,60s:7d")
	m := s.Add("a.x.y", "5m:90d")
	m.Created = now.Add(-time.Hour)

	// raw: aligned window, 10s rung, deterministic values
	code, body := get(t, s, "/render?target=a.b.c&from=-25s&until=now&format=raw")
	if code != 200 || !strings.HasPrefix(body, "a.b.c,1787349980,1787350010,10|") {
		t.Errorf("raw: %d %q", code, body)
	}
	// the `now` parameter pins the reference time; 60s rung beyond 6h
	_, body = get(t, s, "/render?target=a.b.c&from=1787300000&until=1787300001&now=1787350000&format=raw")
	if !strings.HasPrefix(body, "a.b.c,1787300040,1787300100,60|") {
		t.Errorf("now param / 60s rung: %q", body)
	}
	// beyond retention is empty; clamped when until is inside
	_, body = get(t, s, "/render?target=a.b.c&from=-9d&until=-8d&format=raw")
	if body != "" {
		t.Errorf("beyond retention: %q", body)
	}
	_, body = get(t, s, "/render?target=a.b.c&from=-9d&until=now&format=raw&maxDataPoints=2")
	if !strings.HasPrefix(body, "a.b.c,1786745220,") {
		t.Errorf("clamped: %q", body)
	}
	// young metric: nulls before Created
	_, body = get(t, s, "/render?target=a.x.y&from=-2h&until=now&format=raw")
	if !strings.Contains(body, "None") || !strings.Contains(body, ",300|") {
		t.Errorf("young metric: %q", body)
	}
	// json and wildcards
	_, body = get(t, s, "/render?target=a.b.*&from=-25s&until=now&format=json")
	var js []struct {
		Target     string   `json:"target"`
		Datapoints [][2]any `json:"datapoints"`
	}
	if err := json.Unmarshal([]byte(body), &js); err != nil || len(js) != 2 || len(js[0].Datapoints) != 3 {
		t.Errorf("json: %v %q", err, body)
	}
	// braces
	_, body = get(t, s, "/render?target=a.{b,x}.{c,y}&from=-25s&until=now&format=raw")
	if strings.Count(body, "\n") != 2 {
		t.Errorf("braces: %q", body)
	}
	// errors
	if code, _ := get(t, s, "/render?target=a.b.c&from=-5m&format=raw"); code != 400 {
		t.Error("bad from must be 400")
	}
	if code, _ := get(t, s, "/render?target=a.b.c&now=bogus&format=raw"); code != 400 {
		t.Error("bad now must be 400")
	}
	// find
	_, body = get(t, s, "/metrics/find?query=a.*")
	if !strings.Contains(body, `"id":"a.b"`) || !strings.Contains(body, `"leaf":0`) {
		t.Errorf("find branches: %q", body)
	}
	_, body = get(t, s, "/metrics/find?query=a.b.*")
	if !strings.Contains(body, `"id":"a.b.c"`) || !strings.Contains(body, `"leaf":1`) {
		t.Errorf("find leaves: %q", body)
	}
	_, body = get(t, s, "/metrics/find?query=zzz")
	if body != "[]\n" {
		t.Errorf("find nothing: %q", body)
	}
	// counters, log, failure mode, removal
	if s.Renders.Load() == 0 || s.Finds.Load() != 3 || len(s.Requests()) == 0 {
		t.Error("counters")
	}
	s.Fail.Store(503)
	if code, _ := get(t, s, "/render?target=a.b.c&format=raw"); code != 503 {
		t.Error("fail render")
	}
	if code, _ := get(t, s, "/metrics/find?query=a"); code != 503 {
		t.Error("fail find")
	}
	s.Fail.Store(0)
	s.ResetCounters()
	if s.Renders.Load() != 0 || len(s.Requests()) != 0 {
		t.Error("reset")
	}
	s.Remove("a.b.c")
	if _, body := get(t, s, "/render?target=a.b.c&from=-25s&format=raw"); body != "" {
		t.Error("removed metric must not render")
	}
	if Value("x", time.Unix(86401, 0)) != 2 {
		t.Error("value")
	}
	defer func() {
		if recover() == nil {
			t.Error("Add with invalid retentions must panic")
		}
	}()
	s.Add("bad", "nonsense")
}

func TestExpand(t *testing.T) {
	// /metrics/expand is the endpoint that turns a wildcard into concrete
	// leaves; /metrics/find deliberately does not expand multi-level wildcards
	s := New()
	defer s.Close()
	s.Add("a.b.c", "10s:6h")
	s.Add("a.b.d", "10s:6h")
	s.Add("a.x.y", "5m:90d")

	code, body := get(t, s, "/metrics/expand?leavesOnly=1&query=a.b.*")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Results []string `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	slices.Sort(out.Results)
	if !slices.Equal(out.Results, []string{"a.b.c", "a.b.d"}) {
		t.Errorf("a.b.*: %v", out.Results)
	}
	// multi-level and brace patterns, which is the whole point of expand
	_, body = get(t, s, "/metrics/expand?leavesOnly=1&query=a.*.*")
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 3 {
		t.Errorf("a.*.*: %v", out.Results)
	}
	_, body = get(t, s, "/metrics/expand?leavesOnly=1&query=a.{b,x}.c")
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Results, []string{"a.b.c"}) {
		t.Errorf("brace: %v", out.Results)
	}
	// nothing matching is an empty list, not an error or a null
	_, body = get(t, s, "/metrics/expand?leavesOnly=1&query=z.*")
	if !strings.Contains(body, `"results":[]`) {
		t.Errorf("no match: %q", body)
	}
	// a configured failure is reported as one
	s.Fail.Store(503)
	if code, _ = get(t, s, "/metrics/expand?query=a.b.*"); code != 503 {
		t.Errorf("failure injection: %d", code)
	}
}
