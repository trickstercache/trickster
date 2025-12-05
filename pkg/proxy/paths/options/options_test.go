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

package options

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

func TestNew(t *testing.T) {

	pc := New()
	require.NotNil(t, pc)

	if pc.HandlerName != providers.Proxy {
		t.Errorf("expected value %s, got %s", providers.Proxy, pc.HandlerName)
	}

}

func TestPathClone(t *testing.T) {

	pc := New()
	pc2 := pc.Clone()
	require.NotNil(t, pc2)

	if pc2.HandlerName != providers.Proxy {
		t.Errorf("expected value %s, got %s", providers.Proxy, pc2.HandlerName)
	}

}

func TestInitialize(t *testing.T) {
	tests := []struct {
		name          string
		options       *Options
		expectedError error
	}{
		{
			name:          "default options",
			options:       New(),
			expectedError: nil,
		},
		{
			name: "options with custom methods",
			options: func() *Options {
				o := New()
				o.Methods = []string{"GET", "POST"}
				return o
			}(),
			expectedError: nil,
		},
		{
			name: "options with empty methods",
			options: func() *Options {
				o := New()
				o.Methods = []string{}
				return o
			}(),
			expectedError: nil,
		},
		{
			name: "options with invalid match type",
			options: func() *Options {
				o := New()
				o.MatchTypeName = "invalid"
				return o
			}(),
			expectedError: nil, // Should default to exact
		},
		{
			name: "options with invalid collapsed forwarding",
			options: func() *Options {
				o := New()
				o.CollapsedForwardingName = "invalid"
				return o
			}(),
			expectedError: nil,
		},
		{
			name: "options with response body",
			options: func() *Options {
				o := New()
				responseBody := "test response"
				o.ResponseBody = &responseBody
				return o
			}(),
			expectedError: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.options.Initialize("")

			if test.expectedError != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", test.expectedError)
					return
				}
				if err.Error() != test.expectedError.Error() {
					t.Errorf("expected error %v, got %v", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLookupInitialize(t *testing.T) {
	tests := []struct {
		name          string
		lookup        Lookup
		expectedError error
	}{
		{
			name:          "empty lookup",
			lookup:        Lookup{},
			expectedError: nil,
		},
		{
			name: "lookup with valid options",
			lookup: Lookup{
				"test": New(),
			},
			expectedError: nil,
		},
		{
			name: "lookup with invalid options",
			lookup: Lookup{
				"test": func() *Options {
					o := New()
					o.CollapsedForwardingName = "invalid"
					return o
				}(),
			},
			expectedError: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.lookup.Initialize()

			if test.expectedError != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", test.expectedError)
					return
				}
				if err.Error() != test.expectedError.Error() {
					t.Errorf("expected error %v, got %v", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name          string
		options       *Options
		expectedError string
	}{
		{
			name:          "valid options",
			options:       New(),
			expectedError: "",
		},
		{
			name: "invalid collapsed forwarding",
			options: func() *Options {
				o := New()
				o.CollapsedForwardingName = "invalid"
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "invalid collapsed_forwarding name: invalid",
		},
		{
			name: "invalid HTTP method",
			options: func() *Options {
				o := New()
				o.Methods = []string{"GET", "INVALID_METHOD"}
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "invalid HTTP method: INVALID_METHOD",
		},
		{
			name: "invalid response code",
			options: func() *Options {
				o := New()
				o.ResponseCode = 999
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "invalid response_code: 999 (must be between 100 and 599)",
		},
		{
			name: "valid response code",
			options: func() *Options {
				o := New()
				o.ResponseCode = 404
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.options.Validate()

			if test.expectedError != "" {
				if err == nil {
					t.Errorf("expected '%s', got nil", test.expectedError)
					return
				}
				if !strings.Contains(err.Error(), test.expectedError) {
					t.Errorf("expected '%s', got '%v'", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
