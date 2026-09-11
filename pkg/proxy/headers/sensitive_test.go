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

package headers

import (
	"strings"
	"testing"
)

func TestHideAuthorizationCredentials(t *testing.T) {
	// matching is canonical and covers signed '+'/'-' update operators
	hdrs := map[string]string{
		NameAuthorization:                        "Basic SomeHash",
		strings.ToLower(NameAuthorization):       "Bearer lower-secret",
		"aUtHoRiZaTiOn":                          "Bearer mixed-secret",
		"+" + NameAuthorization:                  "Bearer append-secret",
		"+" + strings.ToLower(NameAuthorization): "Bearer append-lower-secret",
		"-" + NameAuthorization:                  "Bearer remove-secret",
		"X-Other":                                "kept",
	}
	HideAuthorizationCredentials(hdrs)
	for k, v := range hdrs {
		if k == "X-Other" {
			if v != "kept" {
				t.Errorf("non-sensitive header must be preserved, got '%s'", v)
			}
			continue
		}
		if v != "*****" {
			t.Errorf("expected '*****' for key '%s', got '%s'", k, v)
		}
	}
	// an empty value is a credential opt-out, not a credential; it is preserved
	hdrs = map[string]string{NameAuthorization: "", strings.ToLower(NameAuthorization): ""}
	HideAuthorizationCredentials(hdrs)
	for k, v := range hdrs {
		if v != "" {
			t.Errorf("expected empty value preserved for key '%s', got '%s'", k, v)
		}
	}
}
