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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type field struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels,omitempty"`
}

type frame struct {
	Schema struct {
		Name   string  `json:"name"`
		RefID  string  `json:"refId"`
		Fields []field `json:"fields"`
	} `json:"schema"`
	Data struct {
		Values [][]any `json:"values"`
		Nanos  [][]any `json:"nanos,omitempty"`
	} `json:"data"`
}

type queryResult struct {
	Status int     `json:"status"`
	Error  string  `json:"error"`
	Frames []frame `json:"frames"`
}

type queryResponse struct {
	Results map[string]queryResult `json:"results"`
}

type grafanaClient struct {
	baseURL string
	client  *http.Client
}

func (g grafanaClient) request(method, path string, body any, dst any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, g.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	const maxBody = 16 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		return fmt.Errorf("%s: response exceeds %d bytes", path, maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %.512s", path, resp.StatusCode, raw)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s: trailing JSON data", path)
	}
	return nil
}

func validateResponse(doc queryResponse, refs []string) error {
	if len(refs) == 0 || len(doc.Results) != len(refs) {
		return fmt.Errorf("result refs do not match requested refs %v", refs)
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref == "" || seen[ref] {
			return fmt.Errorf("empty or duplicate ref %q", ref)
		}
		seen[ref] = true
		r, ok := doc.Results[ref]
		if !ok || r.Status != http.StatusOK || r.Error != "" || len(r.Frames) == 0 {
			return fmt.Errorf("ref %s: missing or unsuccessful result (status %d, error %q)", ref, r.Status, r.Error)
		}
		for i, f := range r.Frames {
			fields, columns := len(f.Schema.Fields), len(f.Data.Values)
			if f.Schema.RefID != ref || fields == 0 || fields != columns {
				return fmt.Errorf("ref %s frame %d: invalid schema/column count", ref, i)
			}
			rows := len(f.Data.Values[0])
			if rows == 0 {
				return fmt.Errorf("ref %s frame %d: empty rows", ref, i)
			}
			hasValue := false
			for j, col := range f.Data.Values {
				if len(col) != rows {
					return fmt.Errorf("ref %s frame %d column %d: inconsistent row count", ref, i, j)
				}
				for _, v := range col {
					hasValue = hasValue || (v != nil && f.Schema.Fields[j].Type != "time")
				}
			}
			if !hasValue {
				return fmt.Errorf("ref %s frame %d: no non-null data values", ref, i)
			}
			if len(f.Data.Nanos) != 0 && len(f.Data.Nanos) != columns {
				return fmt.Errorf("ref %s frame %d: invalid nanosecond column count", ref, i)
			}
			for _, col := range f.Data.Nanos {
				if col != nil && len(col) != rows {
					return fmt.Errorf("ref %s frame %d: inconsistent nanosecond row count", ref, i)
				}
			}
		}
	}
	return nil
}

func compareResponses(left, right queryResponse) error {
	_, err := compareFrames(left, right, false)
	return err
}

func compareFrames(left, right queryResponse, allowPercentageRounding bool) (int, error) {
	rounded := 0
	if len(left.Results) != len(right.Results) {
		return 0, fmt.Errorf("Grafana result counts differ")
	}
	for ref, l := range left.Results {
		r, ok := right.Results[ref]
		if !ok || len(l.Frames) != len(r.Frames) {
			return 0, fmt.Errorf("ref %s: frame counts differ", ref)
		}
		for i, f := range l.Frames {
			if !reflect.DeepEqual(f.Schema, r.Frames[i].Schema) {
				return 0, fmt.Errorf("ref %s frame %d: schemas differ", ref, i)
			}
			other := r.Frames[i].Data
			if !reflect.DeepEqual(f.Data.Nanos, other.Nanos) || len(f.Data.Values) != len(other.Values) {
				return 0, fmt.Errorf("ref %s frame %d: nanoseconds or column counts differ", ref, i)
			}
			for col, values := range f.Data.Values {
				if len(values) != len(other.Values[col]) {
					return 0, fmt.Errorf("ref %s frame %d column %d: row counts differ", ref, i, col)
				}
				for row, value := range values {
					actual := other.Values[col][row]
					if reflect.DeepEqual(value, actual) {
						continue
					}
					if allowPercentageRounding && col < len(f.Schema.Fields) &&
						f.Schema.Fields[col].Name == "card_use_rate" && f.Schema.Fields[col].Type == "number" &&
						adjacentPercentages(value, actual) {
						rounded++
						continue
					}
					return 0, fmt.Errorf("ref %s frame %d column %d row %d: value %v differs from %v", ref, i, col, row, value, actual)
				}
			}
		}
	}
	return rounded, nil
}

// PostgreSQL numeric division and DataFusion floating division can round a
// percentage to adjacent float64 values. Never apply this to counts or times.
func adjacentPercentages(left, right any) bool {
	a, ok := left.(json.Number)
	if !ok {
		return false
	}
	b, ok := right.(json.Number)
	if !ok {
		return false
	}
	x, errX := a.Float64()
	y, errY := b.Float64()
	if errX != nil || errY != nil || !(x >= 0 && x <= 100 && y >= 0 && y <= 100) {
		return false
	}
	return x == y || math.Nextafter(x, y) == y
}

const validResponse = `{"results":{"A":{"status":200,"frames":[{"schema":{"refId":"A","fields":[{"name":"time","type":"time"},{"name":"count","type":"number","labels":{"job":"prom"}}]},"data":{"values":[[1000,2000],[9007199254740993,2]],"nanos":[[1,2],null]}}]}}}`

func TestPercentageRounding(t *testing.T) {
	const exact, adjacent = "37.37373737373737", "37.37373737373738"
	raw := strings.ReplaceAll(strings.Replace(validResponse, `"name":"count"`, `"name":"card_use_rate"`, 1), "9007199254740993", exact)
	left := decodeFixture(t, raw)
	right := decodeFixture(t, strings.Replace(raw, exact, adjacent, 1))
	if err := compareResponses(left, right); err == nil {
		t.Fatal("strict comparison accepted different numbers")
	}
	if n, err := compareFrames(left, right, true); err != nil || n != 1 {
		t.Fatalf("rounded cells = %d, error = %v", n, err)
	}
	for _, tt := range []struct{ name, old, replacement string }{
		{"two_steps", exact, "37.373737373737385"},
		{"null", exact, "null"},
		{"timestamp", "[1000,2000]", "[1001,2000]"},
		{"nanoseconds", `"nanos":[[1,2],null]`, `"nanos":[[1,3],null]`},
		{"label", `"job":"prom"`, `"job":"other"`},
		{"field_type", `"type":"number"`, `"type":"string"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := compareFrames(left, decodeFixture(t, strings.Replace(raw, tt.old, tt.replacement, 1)), true); err == nil {
				t.Fatal("accepted a material difference")
			}
		})
	}
	if _, err := compareFrames(decodeFixture(t, validResponse), decodeFixture(t, strings.Replace(validResponse, "9007199254740993", "9007199254740992", 1)), true); err == nil {
		t.Fatal("percentage tolerance lost integer precision")
	}
}

func TestAdjacentPercentages(t *testing.T) {
	for _, tt := range []struct{ name, left, right string }{
		{"negative", "-1", "-1.0000000000000002"},
		{"above_100", "101", "101.00000000000001"},
		{"nan", "NaN", "NaN"},
		{"infinity", "+Inf", "+Inf"},
		{"overflow", "1e9999", "1e9999"},
		{"malformed", "not-a-number", "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if adjacentPercentages(json.Number(tt.left), json.Number(tt.right)) {
				t.Fatal("accepted invalid percentage")
			}
		})
	}
	if adjacentPercentages(nil, json.Number("1")) || adjacentPercentages(json.Number("1"), "1") {
		t.Fatal("accepted nonnumeric type")
	}
}

func decodeFixture(t *testing.T, raw string) queryResponse {
	t.Helper()
	var doc queryResponse
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestValidateResponse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		refs []string
		ok   bool
	}{
		{"valid", validResponse, []string{"A"}, true},
		{"no_requested_refs", validResponse, nil, false},
		{"duplicate_requested_ref", validResponse, []string{"A", "A"}, false},
		{"missing_requested_ref", validResponse, []string{"A", "B"}, false},
		{"empty_result", `{"results":{}}`, []string{"A"}, false},
		{"query_error_in_http_200", strings.Replace(validResponse, `"status":200`, `"status":200,"error":"query failed"`, 1), []string{"A"}, false},
		{"query_failed_status", strings.Replace(validResponse, `"status":200`, `"status":400`, 1), []string{"A"}, false},
		{"empty_frames", `{"results":{"A":{"status":200,"frames":[]}}}`, []string{"A"}, false},
		{"empty_rows", strings.Replace(validResponse, `[[1000,2000],[9007199254740993,2]]`, `[[],[]]`, 1), []string{"A"}, false},
		{"ragged_columns", strings.Replace(validResponse, `[9007199254740993,2]`, `[2]`, 1), []string{"A"}, false},
		{"missing_column", strings.Replace(validResponse, `[[1000,2000],[9007199254740993,2]]`, `[[1000,2000]]`, 1), []string{"A"}, false},
		{"wrong_frame_ref", strings.Replace(validResponse, `"refId":"A"`, `"refId":"B"`, 1), []string{"A"}, false},
		{"ragged_nanos", strings.Replace(validResponse, `"nanos":[[1,2],null]`, `"nanos":[[1],null]`, 1), []string{"A"}, false},
		{"wrong_nanos_width", strings.Replace(validResponse, `"nanos":[[1,2],null]`, `"nanos":[[1,2]]`, 1), []string{"A"}, false},
		{"all_null_values", strings.Replace(validResponse, `[9007199254740993,2]`, `[null,null]`, 1), []string{"A"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateResponse(decodeFixture(t, tt.raw), tt.refs)
			if (err == nil) != tt.ok {
				t.Fatalf("error = %v, want success %v", err, tt.ok)
			}
		})
	}
}

func TestCompareResponses(t *testing.T) {
	for _, tt := range []struct{ name, old, replacement string }{
		{"integer_precision", "9007199254740993", "9007199254740992"},
		{"timestamp_precision", `"nanos":[[1,2],null]`, `"nanos":[[1,3],null]`},
		{"timestamp", "[1000,2000]", "[1001,2000]"},
		{"value", "9007199254740993", "1"},
		{"label", `"job":"prom"`, `"job":"other"`},
		{"field_name", `"name":"count"`, `"name":"other"`},
		{"field_type", `"type":"number"`, `"type":"string"`},
		{"row_order", "[1000,2000]", "[2000,1000]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := decodeFixture(t, validResponse)
			b := decodeFixture(t, strings.Replace(validResponse, tt.old, tt.replacement, 1))
			if compareResponses(a, b) == nil {
				t.Fatal("changed data passed comparison")
			}
		})
	}
	if err := compareResponses(decodeFixture(t, validResponse), decodeFixture(t, validResponse)); err != nil {
		t.Fatal(err)
	}
}

func TestGrafanaRequest(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"valid", validResponse, 200, true},
		{"http_error", validResponse, 500, false},
		{"html_login", "<html>Login</html>", 200, false},
		{"truncated", `{"results":`, 200, false},
		{"trailing_json", validResponse + `{}`, 200, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/ds/query" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer s.Close()
			g := grafanaClient{s.URL, &http.Client{Timeout: time.Second}}
			var doc queryResponse
			err := g.request(http.MethodPost, "/api/ds/query", map[string]any{"queries": []any{}}, &doc)
			if (err == nil) != tt.ok {
				t.Fatalf("error = %v, want success %v", err, tt.ok)
			}
			if tt.ok && doc.Results["A"].Frames[0].Data.Values[1][0] != json.Number("9007199254740993") {
				t.Fatal("lost integer precision")
			}
		})
	}
}
