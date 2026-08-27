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

package negative

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	cfg := New()
	require.NotNil(t, cfg)
	require.Empty(t, cfg)
}

func TestConfigClone(t *testing.T) {
	original := Config{
		"404": time.Minute,
		"500": 2 * time.Minute,
	}
	cloned := original.Clone()

	require.Equal(t, original, cloned)

	cloned["404"] = time.Hour
	require.Equal(t, time.Minute, original["404"])
}

func TestLookupsGet(t *testing.T) {
	lookups := Lookups{
		"default": Lookup{404: time.Minute},
	}
	require.Equal(t, Lookup{404: time.Minute}, lookups.Get("default"))
	require.Nil(t, lookups.Get("missing"))
}

func TestConfigLookupValidateAndCompile(t *testing.T) {
	cases := []struct {
		name    string
		input   ConfigLookup
		want    Lookups
		wantErr string
	}{
		{
			name:  "empty",
			input: ConfigLookup{},
			want:  Lookups{},
		},
		{
			name: "valid single entry",
			input: ConfigLookup{
				"default": Config{
					"404": time.Minute,
					"500": 2 * time.Minute,
				},
			},
			want: Lookups{
				"default": Lookup{
					404: time.Minute,
					500: 2 * time.Minute,
				},
			},
		},
		{
			name: "valid boundary codes",
			input: ConfigLookup{
				"default": Config{
					"400": time.Second,
					"599": time.Second,
				},
			},
			want: Lookups{
				"default": Lookup{
					400: time.Second,
					599: time.Second,
				},
			},
		},
		{
			name: "multiple named caches",
			input: ConfigLookup{
				"default": Config{"404": time.Minute},
				"api":     Config{"503": 30 * time.Second},
			},
			want: Lookups{
				"default": Lookup{404: time.Minute},
				"api":     Lookup{503: 30 * time.Second},
			},
		},
		{
			name: "invalid non-numeric code",
			input: ConfigLookup{
				"default": Config{"a": time.Minute},
			},
			wantErr: `invalid negative_cache config in default: a is not a valid HTTP status code >= 400 and < 600`,
		},
		{
			name: "invalid code below 400",
			input: ConfigLookup{
				"default": Config{"399": time.Minute},
			},
			wantErr: `invalid negative_cache config in default: 399 is not a valid HTTP status code >= 400 and < 600`,
		},
		{
			name: "invalid code at or above 600",
			input: ConfigLookup{
				"default": Config{"600": time.Minute},
			},
			wantErr: `invalid negative_cache config in default: 600 is not a valid HTTP status code >= 400 and < 600`,
		},
		{
			name: "invalid code in named cache",
			input: ConfigLookup{
				"foo": Config{"1212": time.Minute},
			},
			wantErr: `invalid negative_cache config in foo: 1212 is not a valid HTTP status code >= 400 and < 600`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.input.ValidateAndCompile()
			if c.wantErr != "" {
				require.Error(t, err)
				require.Equal(t, c.wantErr, err.Error())
				var ierr *ErrInvalidConfig
				require.True(t, errors.As(err, &ierr))
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

func TestNewErrInvalidConfig(t *testing.T) {
	err := NewErrInvalidConfig("test-cache", "abc")
	require.Error(t, err)
	require.Equal(t,
		`invalid negative_cache config in test-cache: abc is not a valid HTTP status code >= 400 and < 600`,
		err.Error())
	require.IsType(t, &ErrInvalidConfig{}, err)
}
