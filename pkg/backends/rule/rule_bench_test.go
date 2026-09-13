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

package rule

import (
	"context"
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

func benchRule(b testing.TB, o *ro.Options) *rule {
	b.Helper()
	cl1, _ := NewClient("test-backend-1", nil, testMux1, nil, nil, nil)
	cl2, _ := NewClient("test-backend-2", nil, testMux2, nil, nil, nil)
	clients := backends.Backends{"test-backend-1": cl1, "test-backend-2": cl2}
	bc, err := NewClient("bench-client", bo.New(), nil, nil, clients, nil)
	if err != nil {
		b.Fatal(err)
	}
	c := bc.(*Client)
	if err := c.parseOptions(o, newTestRewriterInstructions()); err != nil {
		b.Fatal(err)
	}
	return c.rule
}

func benchOpts(source, key, inputType, op, arg string) *ro.Options {
	return &ro.Options{
		Name: "bench", InputSource: source, InputKey: key, InputType: inputType,
		Operation: op, OperationArg: arg, NextRoute: "test-backend-1",
		CaseOptions: ro.CaseOptionsList{{Matches: []string{"true"}, NextRoute: "test-backend-2"}},
	}
}

func benchRequest() *http.Request {
	hr, _ := http.NewRequest(http.MethodGet, "http://example.com/?q=trickster", nil)
	hr.Header.Set(testRuleHeader, "trickster")
	return hr.WithContext(tc.WithHops(context.Background(), 0, 20))
}

func benchEvaluate(b *testing.B, o *ro.Options) {
	b.Helper()
	r := benchRule(b, o)
	hr := benchRequest()
	b.ReportAllocs()
	for b.Loop() {
		r.EvaluateOpArg(hr)
	}
}

func BenchmarkEvaluateHeaderEq(b *testing.B) {
	benchEvaluate(b, benchOpts("header", testRuleHeader, "string", "eq", "trickster"))
}

func BenchmarkEvaluateHeaderRMatch(b *testing.B) {
	benchEvaluate(b, benchOpts("header", testRuleHeader, "string", "rmatch", "^trick.*$"))
}

func BenchmarkEvaluateHeaderPresence(b *testing.B) {
	benchEvaluate(b, benchOpts("has_header", testRuleHeader, "bool", "eq", "true"))
}

func BenchmarkEvaluateParamEq(b *testing.B) {
	benchEvaluate(b, benchOpts("param", "q", "string", "eq", "trickster"))
}

func BenchmarkEvaluateParamPresence(b *testing.B) {
	benchEvaluate(b, benchOpts("has_param", "q", "bool", "eq", "true"))
}
