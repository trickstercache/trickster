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
	"errors"
	"testing"

	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func TestAuthHeader(t *testing.T) {
	tests := []struct {
		name     string
		o        *Options
		expected string
		err      error
	}{
		{"nil options", nil, "", nil},
		{"unset", &Options{}, "", nil},
		{"basic", &Options{OriginUsername: "u", OriginPassword: "p"},
			"Basic dTpw", nil},
		{"username only", &Options{OriginUsername: "u"}, "Basic dTo=", nil},
		{"raw", &Options{OriginAuthorization: "Bearer tok"}, "Bearer tok", nil},
		{"conflict", &Options{OriginAuthorization: "Bearer tok",
			OriginUsername: "u"}, "", ErrOriginAuthConflict},
		{"password without username", &Options{OriginPassword: "p"},
			"", ErrOriginAuthNoUser},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.o.AuthHeader()
			if !errors.Is(err, tc.err) || out != tc.expected {
				t.Fatalf("got (%q, %v), want (%q, %v)", out, err, tc.expected, tc.err)
			}
		})
	}
}

func TestValidateWithPaths(t *testing.T) {
	authed := &Options{OriginAuthorization: "Bearer tok"}
	if err := authed.ValidateWithPaths(nil); err != nil {
		t.Fatalf("credential with no paths must validate: %v", err)
	}
	if err := (&Options{}).ValidateWithPaths(po.List{
		{Path: "/render", RequestHeaders: map[string]string{"+Authorization": "x"}},
	}); err != nil {
		t.Fatalf("append without a credential must validate: %v", err)
	}
	bad := &Options{OriginAuthorization: "Bearer tok", OriginUsername: "u"}
	if err := bad.ValidateWithPaths(nil); !errors.Is(err, ErrOriginAuthConflict) {
		t.Fatalf("expected ErrOriginAuthConflict, got %v", err)
	}
	paths := po.List{
		nil,
		{Path: "/ok", RequestHeaders: map[string]string{"Authorization": "x"}},
		{Path: "/render", RequestHeaders: map[string]string{"+authorization": "x"}},
	}
	// the signed operation must be detected case-insensitively
	if err := authed.ValidateWithPaths(paths); !errors.Is(err, ErrOriginAuthAppend) {
		t.Fatalf("expected ErrOriginAuthAppend, got %v", err)
	}
}
