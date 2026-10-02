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

package reserved

import "testing"

func TestNamePrefixes(t *testing.T) {
	prefixes := NamePrefixes()
	if len(prefixes) == 0 || prefixes[0] != NamePrefixKubeGateway {
		t.Fatalf("prefixes = %v; want %q first", prefixes, NamePrefixKubeGateway)
	}
	prefixes[0] = "changed"
	if NamePrefixes()[0] != NamePrefixKubeGateway {
		t.Error("NamePrefixes returned shared backing storage")
	}
	for _, p := range NamePrefixes() {
		if !IsNamePrefix(p) {
			t.Errorf("IsNamePrefix(%q) = false", p)
		}
	}
	if IsNamePrefix("") || IsNamePrefix("kgw") {
		t.Error("non-reserved prefix reported as reserved")
	}
	if got := MatchNamePrefix(NamePrefixKubeGateway + "svc"); got != NamePrefixKubeGateway {
		t.Errorf("MatchNamePrefix = %q; want %q", got, NamePrefixKubeGateway)
	}
	if got := MatchNamePrefix("svc"); got != "" {
		t.Errorf("MatchNamePrefix = %q; want empty", got)
	}
}

func TestReferences(t *testing.T) {
	references := References()
	if len(references) == 0 || references[0] != ReferenceNone {
		t.Fatalf("references = %v; want %q first", references, ReferenceNone)
	}
	references[0] = "changed"
	if References()[0] != ReferenceNone {
		t.Error("References returned shared backing storage")
	}
	for _, r := range References() {
		if !IsReference(r) {
			t.Errorf("IsReference(%q) = false", r)
		}
	}
	if IsReference("") || IsReference("None") || IsReference("default") {
		t.Error("non-reserved word reported as reserved")
	}
}
