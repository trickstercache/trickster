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

package rewriter

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

func BenchmarkExecuteScalarInstructions(b *testing.B) {
	ri, err := ParseRewriteList(options.RewriteList{
		{"header", "set", "X-Bench", "1"},
		{"hostname", "set", "origin.example.com"},
		{"port", "set", "8443"},
		{"scheme", "set", "https"},
		{"path", "set", "/v2/items"},
		{"method", "set", "GET"},
	})
	if err != nil {
		b.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodGet, "http://example.com:8080/v1/items?a=1", nil)
	b.ReportAllocs()
	for b.Loop() {
		ri.Execute(r)
	}
}

func BenchmarkExecutePathPrefixReplace(b *testing.B) {
	ri, err := ParseRewriteList(options.RewriteList{{"path", "prefix-replace", "/v1", "/v2"}})
	if err != nil {
		b.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodGet, "http://example.com/v1/items", nil)
	b.ReportAllocs()
	for b.Loop() {
		r.URL.Path = "/v1/items"
		ri.Execute(r)
	}
}

func BenchmarkExecuteHeaderAppendDelete(b *testing.B) {
	ri, err := ParseRewriteList(options.RewriteList{
		{"header", "append", "X-Trail", "hop"},
		{"header", "delete", "X-Trail"},
	})
	if err != nil {
		b.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	b.ReportAllocs()
	for b.Loop() {
		ri.Execute(r)
	}
}
