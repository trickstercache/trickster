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
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
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

func TestPathMerge(t *testing.T) {

	pc := New()
	pc2 := pc.Clone()

	expectedPath := "testPath"
	expectedHandlerName := "testHandler"

	pc2.Path = expectedPath
	pc2.MatchType = matching.PathMatchTypePrefix
	pc2.HandlerName = expectedHandlerName
	pc2.Methods = []string{http.MethodPost}
	pc2.CacheKeyParams = []string{"params"}
	pc2.CacheKeyHeaders = []string{"headers"}
	pc2.CacheKeyFormFields = []string{"fields"}
	pc2.RequestHeaders = map[string]string{"header1": "1"}
	pc2.RequestParams = map[string]string{"param1": "foo"}
	pc2.ResponseHeaders = map[string]string{"header2": "2"}
	pc2.ResponseCode = 404
	responseBody := "trickster"
	pc2.ResponseBody = &responseBody
	pc2.NoMetrics = true
	pc2.CollapsedForwardingName = forwarding.CFNameProgressive
	pc2.CollapsedForwardingType = forwarding.CFTypeProgressive

	pc.Merge(pc2)

	if pc.Path != expectedPath {
		t.Errorf("expected %s got %s", expectedPath, pc.Path)
	}

	if pc.MatchType != matching.PathMatchTypePrefix {
		t.Errorf("expected %s got %s", matching.PathMatchTypePrefix, pc.MatchType)
	}

	if pc.HandlerName != expectedHandlerName {
		t.Errorf("expected %s got %s", expectedHandlerName, pc.HandlerName)
	}

	if len(pc.CacheKeyParams) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.CacheKeyParams))
	}

	if len(pc.CacheKeyHeaders) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.CacheKeyHeaders))
	}

	if len(pc.CacheKeyFormFields) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.CacheKeyFormFields))
	}

	if len(pc.RequestHeaders) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.RequestHeaders))
	}

	if len(pc.RequestParams) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.RequestParams))
	}

	if len(pc.ResponseHeaders) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.ResponseHeaders))
	}

	if pc.ResponseCode != 404 {
		t.Errorf("expected %d got %d", 404, pc.ResponseCode)
	}

	if pc.ResponseCode != 404 {
		t.Errorf("expected %d got %d", 404, pc.ResponseCode)
	}

	if pc.ResponseBody == nil || *pc.ResponseBody != "trickster" {
		t.Errorf("expected %s got %v", "trickster", pc.ResponseBody)
	}

	if !pc.NoMetrics {
		t.Errorf("expected %t got %t", true, pc.NoMetrics)
	}

	if pc.CollapsedForwardingName != forwarding.CFNameProgressive ||
		pc.CollapsedForwardingType != forwarding.CFTypeProgressive {
		t.Errorf("expected %s got %s", forwarding.CFNameProgressive, pc.CollapsedForwardingName)
	}

}

func TestMerge(t *testing.T) {

	o := &Options{}
	o2 := &Options{ReqRewriterName: "test_rewriter"}
	o.Merge(o2)

	if o.ReqRewriterName != "test_rewriter" {
		t.Errorf("expected %s got %s", "test_rewriter", o.ReqRewriterName)
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
