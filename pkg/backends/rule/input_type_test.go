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

package rule

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestExtractions(t *testing.T) {
	const scheme = "https"
	const hostname = "example.com"
	const port = "8480"
	const path = "/path1/path2"
	const params = "param1=value"

	const host = hostname + ":" + port
	const testURLNoParams = scheme + "://" + host + path
	const testURL = testURLNoParams + "?" + params

	const testHeaderName = headers.NameAuthorization
	const testHeaderVal = "Basic xyz123base64"

	r, _ := http.NewRequest("GET", testURL, nil)
	r.Header = http.Header{testHeaderName: []string{testHeaderVal}}
	// a field that is present and empty reads the same as an absent one
	// through a value lookup, and differently through a presence lookup
	rEmpty, _ := http.NewRequest("GET", testURLNoParams+"?empty=", nil)
	rEmpty.Header = http.Header{"X-Empty": []string{""}}

	tests := []struct {
		source   string
		inputKey string
		expected string
		request  *http.Request
	}{
		{"method", "", "GET", r},
		{"url", "", testURL, r},
		{"url_no_params", "", testURLNoParams, r},
		{"scheme", "", scheme, r},
		{"host", "", host, r},
		{"hostname", "", hostname, r},
		{"port", "", port, r},
		{"path", "", path, r},
		{"params", "", params, r},
		{"param", "param1", "value", r},
		{"header", headers.NameAuthorization, testHeaderVal, r},
		{"has_param", "param1", "true", r},
		{"has_param", "absent", "false", r},
		{"has_header", headers.NameAuthorization, "true", r},
		{"has_header", "X-Absent", "false", r},
		{"has_header", "X-Empty", "true", rEmpty},
		{"has_param", "empty", "true", rEmpty},
		{"header", "X-Empty", "", rEmpty},
		{"param", "empty", "", rEmpty},
		{"method", "", "", nil},
		{"url", "", "", nil},
		{"url_no_params", "", "", nil},
		{"scheme", "", "", nil},
		{"host", "", "", nil},
		{"hostname", "", "", nil},
		{"port", "", "", nil},
		{"path", "", "", nil},
		{"params", "", "", nil},
		{"param", "param1", "", nil},
		{"header", headers.NameAuthorization, "", nil},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if f, ok := sourceExtractionFuncs[inputType(test.source)]; ok {
				got := f(test.request, test.inputKey)
				if got != test.expected {
					t.Errorf("\ngot      %s\nexpected %s", got, test.expected)
				}
			} else {
				t.Errorf("unknown source %v", test.source)
			}
		})
	}
}

func TestIsValidSourceName(t *testing.T) {
	tests := []struct {
		source   string
		expected bool
	}{
		{"url", true},
		{"URL", false},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, b := isValidSourceName(test.source)
			if b != test.expected {
				t.Errorf("got %t expected %t", b, test.expected)
			}
		})
	}
}

func TestExtractSourcePart(t *testing.T) {
	v := extractSourcePart("part one", " ", 1)
	if v != "one" {
		t.Errorf("expected %s got %s", "one", v)
	}
	v = extractSourcePart("", " ", 1)
	if v != "" {
		t.Errorf("expected %s got %s", "", v)
	}
	v = extractSourcePart("test", " ", 1)
	if v != "" {
		t.Errorf("expected %s got %s", "", v)
	}
}

// a configured header key is canonicalized once at parse time, so any
// spelling of the name reads the header the request carries
func TestHeaderKeyCanonicalizedAtParse(t *testing.T) {
	r := benchRule(t, benchOpts("has_header", "x-tenant", "bool", "eq", "true"))
	hr := benchRequest()
	hr.Header.Set("X-Tenant", "a")
	if r.extractionFunc(hr, r.extractionArg) != "true" {
		t.Error("expected the lowercase key to find the canonical header")
	}
	r = benchRule(t, benchOpts("header", "x-tenant", "string", "eq", "a"))
	if r.extractionFunc(hr, r.extractionArg) != "a" {
		t.Error("expected the lowercase key to read the canonical header")
	}
}
