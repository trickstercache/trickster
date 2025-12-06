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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil options")
	}
}

func TestCloneAndEqual(t *testing.T) {

	o := New()
	o2 := o.Clone()

	if !o.Equal(o2) {
		t.Error("expected true")
	}

	if o.Equal(nil) {
		t.Error("expected false")
	}

}

func TestInitialize(t *testing.T) {
	tests := []struct {
		name             string
		options          *Options
		activeCaches     sets.Set[string]
		expectedWarnings int
		expectedError    error
	}{
		{
			name:             "nil active caches",
			options:          New(),
			activeCaches:     nil,
			expectedWarnings: 0,
			expectedError:    nil,
		},
		{
			name:             "empty active caches",
			options:          New(),
			activeCaches:     sets.NewStringSet(),
			expectedWarnings: 0,
			expectedError:    nil,
		},
		{
			name: "redis standard client with endpoint",
			options: func() *Options {
				o := New()
				o.Provider = "redis"
				o.ProviderID = providers.Redis
				o.Redis.ClientType = "standard"
				o.Redis.Endpoint = "127.0.0.1:6379"
				return o
			}(),
			activeCaches:     sets.New([]string{"default"}),
			expectedWarnings: 0,
			expectedError:    nil,
		},
		{
			name: "redis standard client with endpoints",
			options: func() *Options {
				o := New()
				o.Provider = "redis"
				o.Redis.ClientType = "standard"
				o.Redis.Endpoints = []string{"127.0.0.1:6379"}
				return o
			}(),
			activeCaches:     sets.New([]string{"default"}),
			expectedWarnings: 0,
			expectedError:    nil,
		},
		{
			name: "redis sentinel client with endpoint",
			options: func() *Options {
				o := New()
				o.Provider = "redis"
				o.Redis.ClientType = "sentinel"
				o.Redis.Endpoint = "127.0.0.1:6379"
				return o
			}(),
			activeCaches:     sets.New([]string{"default"}),
			expectedWarnings: 0,
			expectedError:    nil,
		},
		{
			name: "max size backoff bytes too big",
			options: func() *Options {
				o := New()
				o.Index.MaxSizeBackoffBytes = 16384
				o.Index.MaxSizeBytes = 1
				return o
			}(),
			activeCaches:     sets.New([]string{"default"}),
			expectedWarnings: 0,
			expectedError:    nil, // Validation happens in Validate(), not Initialize()
		},
		{
			name: "max size backoff objects too big",
			options: func() *Options {
				o := New()
				o.Index.MaxSizeBackoffObjects = 32768
				o.Index.MaxSizeObjects = 16384
				return o
			}(),
			activeCaches:     sets.New([]string{"default"}),
			expectedWarnings: 0,
			expectedError:    nil, // Validation happens in Validate(), not Initialize()
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l := Lookup{"default": test.options}
			warnings, err := l.Initialize(test.activeCaches)

			if test.expectedError != nil {
				if err != test.expectedError {
					t.Errorf("expected error %v, got %v", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(warnings) != test.expectedWarnings {
				t.Errorf("expected %d warnings, got %d", test.expectedWarnings, len(warnings))
			}
		})
	}
}
