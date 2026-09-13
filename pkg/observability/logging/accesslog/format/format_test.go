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

package format

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func testFields() *Fields {
	return &Fields{
		StartTime:    time.Date(2026, 8, 26, 10, 30, 0, 0, time.UTC),
		Duration:     1503 * time.Millisecond,
		ClientIP:     "203.0.113.9",
		RemoteIP:     "10.0.0.5",
		User:         "frank",
		Method:       http.MethodGet,
		RequestURI:   "/api/query?q=up",
		Path:         "/api/query",
		Query:        "q=up",
		Proto:        "HTTP/1.1",
		Host:         "example.com",
		LocalIP:      "10.0.0.1",
		LocalPort:    "8480",
		Status:       200,
		BytesWritten: 2326,
		ReqHeader: http.Header{
			headers.NameReferer:   []string{"http://example.com/start.html"},
			headers.NameUserAgent: []string{"test-agent/1.0"},
			headers.NameCookie:    []string{"session=abc123; theme=dark"},
		},
		RespHeader: http.Header{
			headers.NameContentType: []string{"application/json"},
		},
		Backend:     "example1",
		Provider:    "rp",
		PathConfig:  "/api",
		CacheStatus: status.StatusPartialHit,
		Engine:      "DeltaProxyCache",
	}
}

func render(t *testing.T, formatStr string, f *Fields) string {
	t.Helper()
	fm, err := ParseFormat(formatStr)
	if err != nil {
		t.Fatalf("format %q: %v", formatStr, err)
	}
	return string(fm.Render(nil, f))
}

func TestTokens(t *testing.T) {
	f := testFields()
	tests := []struct {
		format   string
		expected string
	}{
		{"%h", "203.0.113.9"},
		{"%a", "203.0.113.9"},
		{"%{c}a", "10.0.0.5"},
		{"%l", "-"},
		{"%u", "frank"},
		{"%t", "[26/Aug/2026:10:30:00 +0000]"},
		{"%r", "GET /api/query?q=up HTTP/1.1"},
		{"%m", "GET"},
		{"%U", "/api/query"},
		{"%q", "?q=up"},
		{"%H", "HTTP/1.1"},
		{"%s", "200"},
		{"%>s", "200"},
		{"%b", "2326"},
		{"%B", "2326"},
		{"%D", "1503000"},
		{"%T", "1"},
		{"%{us}T", "1503000"},
		{"%{ms}T", "1503"},
		{"%{s}T", "1"},
		{"%{sec}t", "1787740200"},
		{"%{msec}t", "1787740200000"},
		{"%{usec}t", "1787740200000000"},
		{"%{2006-01-02}t", "2026-08-26"},
		{"%{Referer}i", "http://example.com/start.html"},
		{"%{user-agent}i", "test-agent/1.0"},
		{"%{Missing}i", "-"},
		{"%{Content-Type}o", "application/json"},
		{"%{Missing}o", "-"},
		{"%{session}c", "abc123"},
		{"%{theme}c", "dark"},
		{"%{missing}c", "-"},
		{"%v", "example.com"},
		{"%p", "8480"},
		{"%A", "10.0.0.1"},
		{"%%", "%"},
		{"%{backend}x", "example1"},
		{"%{provider}x", "rp"},
		{"%{cache-status}x", status.StatusPartialHit},
		{"%{engine}x", "DeltaProxyCache"},
		{"%{path-config}x", "/api"},
		{"literal only", "literal only"},
		{"a %m b", "a GET b"},
	}
	for _, test := range tests {
		if out := render(t, test.format, f); out != test.expected+"\n" {
			t.Errorf("format %q: expected %q, got %q",
				test.format, test.expected+"\n", out)
		}
	}
}

func TestEmptyFieldTokens(t *testing.T) {
	f := &Fields{}
	tests := []struct {
		format   string
		expected string
	}{
		{"%h", "-"},
		{"%u", "-"},
		{"%b", "-"},
		{"%B", "0"},
		{"%q", ""},
		{"%{User-Agent}i", "-"},
		{"%{Set-Cookie}o", "-"},
		{"%{session}c", "-"},
		{"%{cache-status}x", "-"},
		{"%{engine}x", "-"},
	}
	for _, test := range tests {
		if out := render(t, test.format, f); out != test.expected+"\n" {
			t.Errorf("format %q: expected %q, got %q",
				test.format, test.expected+"\n", out)
		}
	}
}

var clfRegex = regexp.MustCompile(
	`^(\S+) (\S+) (\S+) \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+|-)`)

func TestCommonPresetCLFConformance(t *testing.T) {
	out := strings.TrimSuffix(render(t, Common, testFields()), "\n")
	m := clfRegex.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("common preset line does not parse as CLF: %q", out)
	}
	if m[1] != "203.0.113.9" || m[3] != "frank" || m[6] != "200" || m[7] != "2326" {
		t.Errorf("unexpected CLF values: %v", m[1:])
	}
}

func TestCombinedPreset(t *testing.T) {
	out := render(t, Combined, testFields())
	expected := `203.0.113.9 - frank [26/Aug/2026:10:30:00 +0000] ` +
		`"GET /api/query?q=up HTTP/1.1" 200 2326 ` +
		`"http://example.com/start.html" "test-agent/1.0"` + "\n"
	if out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestExtendedPreset(t *testing.T) {
	out := render(t, Extended, testFields())
	if !strings.Contains(out, " 1503 phit example1\n") {
		t.Errorf("unexpected extended preset output: %q", out)
	}
}

func TestDefaultFormatIsCombined(t *testing.T) {
	f := testFields()
	if render(t, "", f) != render(t, Combined, f) {
		t.Error("expected empty format to resolve to combined")
	}
}

func TestJSONPreset(t *testing.T) {
	out := render(t, JSON, testFields())
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("json preset produced invalid JSON: %v: %q", err, out)
	}
	expected := map[string]any{
		"client_ip":    "203.0.113.9",
		"remote_ip":    "10.0.0.5",
		"user":         "frank",
		"method":       "GET",
		"path":         "/api/query",
		"query":        "q=up",
		"status":       float64(200),
		"bytes":        float64(2326),
		"duration_ms":  float64(1503),
		"host":         "example.com",
		"user_agent":   "test-agent/1.0",
		"backend":      "example1",
		"cache_status": status.StatusPartialHit,
		"engine":       "DeltaProxyCache",
	}
	for k, v := range expected {
		if m[k] != v {
			t.Errorf("json field %s: expected %v, got %v", k, v, m[k])
		}
	}
	if _, ok := m["time"]; !ok {
		t.Error("expected time field in json output")
	}
}

func TestJSONPresetEscapesInvalidInput(t *testing.T) {
	f := testFields()
	f.Path = "/\x01\xff\b\f"
	f.ReqHeader.Set(headers.NameUserAgent, "agent\x80\v")
	out := render(t, JSON, f)
	if !json.Valid([]byte(out)) {
		t.Fatalf("json preset produced invalid JSON: %q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["path"] != "/\x01�\b\f" {
		t.Errorf("unexpected escaped path: %q", got["path"])
	}
	if got["user_agent"] != "agent�\v" {
		t.Errorf("unexpected escaped user agent: %q", got["user_agent"])
	}
}

func TestEscaping(t *testing.T) {
	f := testFields()
	f.User = `fr"ank\`
	f.RequestURI = "/x\ny\r\tz\x01"
	out := render(t, `%u "%r"`, f)
	expected := `fr\"ank\\ "GET /x\ny\r\tz\x01 HTTP/1.1"` + "\n"
	if out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestParseFormatErrors(t *testing.T) {
	for _, s := range []string{
		"%", "%Z", "%{Referer", "%{Referer}", "%{Referer}Z", "%>x", "%>",
		"%{}t", "%{nope}T", "%{nope}x", "%{x}a", "trailing %",
	} {
		if _, err := ParseFormat(s); !errors.Is(err, ErrInvalidFormatToken) {
			t.Errorf("format %q: expected ErrInvalidFormatToken, got %v", s, err)
		}
	}
}

func TestRenderReusesBuffer(t *testing.T) {
	fm, err := ParseFormat("%m %s")
	if err != nil {
		t.Fatal(err)
	}
	f := testFields()
	b := fm.Render(make([]byte, 0, 64), f)
	b2 := fm.Render(b[:0], f)
	if string(b2) != "GET 200\n" {
		t.Errorf("unexpected render output: %q", string(b2))
	}
}

func TestNeedsResultHeader(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   bool
	}{
		{format: "%m %s"},
		{format: "%{engine}x", want: true},
		{format: "%{cache-status}x", want: true},
		{format: JSON, want: true},
	} {
		fm, err := ParseFormat(tc.format)
		if err != nil {
			t.Fatal(err)
		}
		if got := fm.NeedsResultHeader(); got != tc.want {
			t.Errorf("format %q needs result = %t, want %t", tc.format, got, tc.want)
		}
	}
}

func FuzzParseFormat(f *testing.F) {
	for _, seed := range []string{
		presets[Common], presets[Combined], presets[Extended],
		"%{Referer}i %% %{sec}t", "%", "%{", "plain",
	} {
		f.Add(seed)
	}
	fields := testFieldsForFuzz()
	f.Fuzz(func(t *testing.T, input string) {
		fm, err := ParseFormat(input)
		if err != nil {
			return
		}
		// a successfully parsed format must render without panicking
		fm.Render(nil, fields)
	})
}

func testFieldsForFuzz() *Fields {
	return &Fields{
		StartTime: time.Unix(1700000000, 0),
		ReqHeader: http.Header{headers.NameCookie: []string{"a=b"}},
	}
}

func TestExtendedTokens(t *testing.T) {
	f := testFields()
	f.UpstreamAddr = "10.1.1.1:9090"
	f.UpstreamStatus = 502
	f.UpstreamDuration = 42 * time.Millisecond
	f.TraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	f.SpanID = "00f067aa0ba902b7"
	f.RequestID = "req-1"
	f.Extra = NewExtra(map[string]string{"route": "ns/name", "kind": "Ingress"})
	tests := []struct {
		format   string
		expected string
	}{
		{"%{upstream-addr}x", "10.1.1.1:9090"},
		{"%{upstream-status}x", "502"},
		{"%{upstream-duration}x", "42"},
		{"%{trace-id}x", "4bf92f3577b34da6a3ce929d0e0e4736"},
		{"%{span-id}x", "00f067aa0ba902b7"},
		{"%{request-id}x", "req-1"},
		{"%{route}e", "ns/name"},
		{"%{kind}e", "Ingress"},
		{"%{missing}e", "-"},
	}
	for _, tc := range tests {
		if got := render(t, tc.format, f); got != tc.expected+"\n" {
			t.Errorf("format %q: expected %q, got %q", tc.format, tc.expected, got)
		}
	}
	empty := &Fields{}
	for _, format := range []string{"%{upstream-addr}x", "%{upstream-status}x",
		"%{upstream-duration}x", "%{trace-id}x", "%{span-id}x", "%{request-id}x", "%{route}e"} {
		if got := render(t, format, empty); got != "-\n" {
			t.Errorf("format %q on empty fields: expected -, got %q", format, got)
		}
	}
	if _, err := ParseFormat("%{}e"); !errors.Is(err, ErrInvalidFormatToken) {
		t.Errorf("expected ErrInvalidFormatToken for an empty extra key, got %v", err)
	}
	if NewExtra(nil) != nil {
		t.Error("expected nil Extra for an empty map")
	}
	if v, ok := f.Extra.Get("kind"); !ok || v != "Ingress" {
		t.Errorf("unexpected extra lookup: %q %t", v, ok)
	}
	if f.Extra[0].Key != "kind" || f.Extra[1].Key != "route" {
		t.Errorf("extra keys must be sorted: %+v", f.Extra)
	}
}

func TestJSONPresetExtendedFields(t *testing.T) {
	f := testFields()
	f.UpstreamAddr = "10.1.1.1:9090"
	f.UpstreamStatus = 200
	f.UpstreamDuration = 7 * time.Millisecond
	f.TraceID = "abc"
	f.RequestID = "req-1"
	out := render(t, JSON, f)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v: %q", err, out)
	}
	if m["upstream_addr"] != "10.1.1.1:9090" || m["upstream_status"] != float64(200) ||
		m["upstream_duration_ms"] != float64(7) || m["trace_id"] != "abc" || m["request_id"] != "req-1" {
		t.Errorf("unexpected extended fields: %v", m)
	}
	if _, ok := m["extra"]; ok {
		t.Error("extra must be omitted when none is declared")
	}
	f.Extra = NewExtra(map[string]string{"route": "ns/name", "k\"ey": "v"})
	out = render(t, JSON, f)
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON with extra: %v: %q", err, out)
	}
	extra, _ := m["extra"].(map[string]any)
	if extra["route"] != "ns/name" || extra[`k"ey`] != "v" {
		t.Errorf("unexpected extra object: %v", m["extra"])
	}
}

func TestNeedsResourcesAndRequestID(t *testing.T) {
	for _, tc := range []struct {
		format    string
		resources bool
		requestID bool
	}{
		{format: "%m %s"},
		{format: "%{engine}x"},
		{format: "%{upstream-addr}x", resources: true},
		{format: "%{upstream-status}x", resources: true},
		{format: "%{upstream-duration}x", resources: true},
		{format: "%{trace-id}x", resources: true},
		{format: "%{span-id}x", resources: true},
		{format: "%{request-id}x", requestID: true},
		{format: "%{route}e"},
		{format: JSON, resources: true, requestID: true},
	} {
		fm, err := ParseFormat(tc.format)
		if err != nil {
			t.Fatal(err)
		}
		if got := fm.NeedsResources(); got != tc.resources {
			t.Errorf("format %q needs resources = %t, want %t", tc.format, got, tc.resources)
		}
		if got := fm.NeedsRequestID(); got != tc.requestID {
			t.Errorf("format %q needs request id = %t, want %t", tc.format, got, tc.requestID)
		}
	}
	var nilFm *Formatter
	if nilFm.NeedsResources() || nilFm.NeedsRequestID() {
		t.Error("nil formatter needs nothing")
	}
}

func BenchmarkRender(b *testing.B) {
	f := testFields()
	f.Extra = NewExtra(map[string]string{"route": "ns/name"})
	for _, name := range []string{Common, Combined, Extended, JSON,
		`%h %u %t "%r" %>s %b %{ms}T %{upstream-addr}x %{upstream-status}x %{route}e`} {
		fm, err := ParseFormat(name)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			buf := make([]byte, 0, 512)
			b.ReportAllocs()
			for b.Loop() {
				buf = fm.Render(buf[:0], f)
			}
		})
	}
}
