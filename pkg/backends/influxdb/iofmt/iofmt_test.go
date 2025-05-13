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

package iofmt

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestIsInfluxQL(t *testing.T) {
	if out := FluxJsonJson.IsInfluxQL(); out {
		t.Error("expected false")
	}
	if out := InfluxqlPost.IsInfluxQL(); !out {
		t.Error("expected true")
	}
}

func TestIsFlux(t *testing.T) {
	if out := FluxJsonJson.IsFlux(); !out {
		t.Error("expected true")
	}
	if out := InfluxqlPost.IsFlux(); out {
		t.Error("expected false")
	}
}

func TestIsFluxInputJSON(t *testing.T) {
	if out := FluxJsonJson.IsFluxInputJSON(); !out {
		t.Error("expected true")
	}
	if out := InfluxqlPost.IsFluxInputJSON(); out {
		t.Error("expected false")
	}
}

func TestIsFluxOutputJSON(t *testing.T) {
	if out := FluxJsonJson.IsFluxOutputJSON(); !out {
		t.Error("expected true")
	}
	if out := FluxRawJson.IsFluxOutputJSON(); !out {
		t.Error("expected true")
	}
	if out := FluxRawCsv.IsFluxOutputJSON(); out {
		t.Error("expected false")
	}
	if out := InfluxqlPost.IsFluxOutputJSON(); out {
		t.Error("expected false")
	}
}

func TestDetect(t *testing.T) {
	emptyHeader := make(http.Header)
	fluxHeader := http.Header{
		headers.NameContentType: []string{headers.ValueApplicationFlux},
	}
	influxqlPostHeader := http.Header{
		headers.NameContentType: []string{headers.ValueXFormURLEncoded},
	}
	fluxHeaderAcceptJSON := http.Header{
		headers.NameContentType: []string{headers.ValueApplicationFlux},
		headers.NameAccept:      []string{headers.ValueApplicationJSON},
	}
	fluxHeaderProvideAcceptJSON := http.Header{
		headers.NameContentType: []string{headers.ValueApplicationJSON},
		headers.NameAccept:      []string{headers.ValueApplicationJSON},
	}
	fluxHeaderProvideJSON := http.Header{
		headers.NameContentType: []string{headers.ValueApplicationJSON},
	}
	unknownPostHeader := http.Header{
		headers.NameContentType: []string{headers.ValueMultipartFormData},
	}
	tests := []struct {
		name     string
		method   string
		headers  http.Header
		expected Format
	}{
		{
			name:     "testFluxInRawOutCSV",
			method:   http.MethodPost,
			headers:  fluxHeader,
			expected: FluxRawCsv,
		},
		{
			name:     "testFluxInRawOutJSON",
			method:   http.MethodPost,
			headers:  fluxHeaderAcceptJSON,
			expected: FluxRawJson,
		},
		{
			name:     "testInfluxqlInGET",
			method:   http.MethodGet,
			headers:  emptyHeader,
			expected: InfluxqlGet,
		},
		{
			name:     "testInfluxqlInPOST",
			method:   http.MethodPost,
			headers:  influxqlPostHeader,
			expected: InfluxqlPost,
		},
		{
			name:     "testFluxInJSONOutJSON",
			method:   http.MethodPost,
			headers:  fluxHeaderProvideAcceptJSON,
			expected: FluxJsonJson,
		},
		{
			name:     "testFluxInJSONOutCSV",
			method:   http.MethodPost,
			headers:  fluxHeaderProvideJSON,
			expected: FluxJsonCsv,
		},
		{
			name:     "testUnknownPostFormat",
			method:   http.MethodPost,
			headers:  unknownPostHeader,
			expected: Unknown,
		},
		{
			name:     "testUnknownVerb",
			method:   http.MethodPatch,
			headers:  emptyHeader,
			expected: Unknown,
		},
	}
	r := &http.Request{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r.Header = test.headers
			r.Method = test.method
			out := Detect(r)
			if out != test.expected {
				t.Errorf("expected %d got %d", test.expected, out)
			}
		})
	}
}
