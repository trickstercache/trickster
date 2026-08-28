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

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
)

func TestCloneYAMLSafe(t *testing.T) {
	o := &Options{Users: ct.EnvStringMap{
		"bob":   "bob-password",
		"alice": "alice-password",
	}}

	got := o.CloneYAMLSafe()

	if len(got.Users) != 2 ||
		got.Users["user1"] != "*****" ||
		got.Users["user2"] != "*****" {
		t.Fatalf("unexpected redacted users: %#v", got.Users)
	}
	if o.Users["alice"] != "alice-password" || o.Users["bob"] != "bob-password" {
		t.Fatalf("CloneYAMLSafe mutated original users: %#v", o.Users)
	}
}
