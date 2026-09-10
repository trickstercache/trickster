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

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
)

func benchInitialize(b *testing.B, path string, name matching.PathMatchName, methods []string) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		o := New()
		o.Path = path
		o.Methods = methods
		o.MatchTypeName = name
		if err := o.Initialize(""); err != nil {
			b.Fatal(err)
		}
		if _, err := o.Validate(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInitializePrefixWildcardMethods(b *testing.B) {
	benchInitialize(b, "/api/", matching.PathMatchNamePrefix, []string{"*"})
}

func BenchmarkInitializeExactMethods(b *testing.B) {
	benchInitialize(b, "/api/items", matching.PathMatchNameExact, []string{"GET", "POST"})
}

func BenchmarkInitializeRegexPath(b *testing.B) {
	benchInitialize(b, "^/api/(v1|v2)/", matching.PathMatchNameRegex, []string{"GET"})
}
