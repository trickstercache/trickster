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
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwo "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

var testMux1 = http.NewServeMux()
var testMux2 = http.NewServeMux()

var testRuleHeader = "Test-Rule-Header"

func newTestRewriterOpts() map[string]*rwo.Options {

	return map[string]*rwo.Options{
		"test-rewriter-1": {
			Instructions: [][]string{
				{
					"header", "set", "Test", "test-rewriter-1",
				},
				{
					"header", "append", "Test-Trail", "test-rewriter-1",
				},
			},
		},
		"test-rewriter-2": {
			Instructions: [][]string{
				{
					"header", "set", "Test", "test-rewriter-2",
				},
				{
					"header", "append", "Test-Trail", "test-rewriter-2",
				},
			},
		},
		"test-rewriter-3": {
			Instructions: [][]string{
				{
					"header", "set", "Test", "test-rewriter-3",
				},
				{
					"header", "append", "Test-Trail", "test-rewriter-3",
				},
			},
		},
		"test-rewriter-4": {
			Instructions: [][]string{
				{
					"header", "set", "Test", "test-rewriter-4",
				},
				{
					"header", "append", "Test-Trail", "test-rewriter-4",
				},
			},
		},
		"test-rewriter-5": {
			Instructions: [][]string{
				{
					"header", "set", "Test", "test-rewriter-5",
				},
				{
					"header", "append", "Test-Trail", "test-rewriter-5",
				},
			},
		},
	}
}

func newTestRewriterInstructions() map[string]rewriter.RewriteInstructions {
	rwi, _ := rewriter.ProcessConfigs(newTestRewriterOpts())
	return rwi
}

func newTestRuleOpts() *ro.Options {
	return &ro.Options{
		Name:                   "test-rule",
		InputType:              "string",
		InputSource:            "header",
		InputKey:               testRuleHeader,
		Operation:              "eq",
		NextRoute:              "test-backend-1",
		IngressReqRewriterName: "test-rewriter-1",
		EgressReqRewriterName:  "test-rewriter-2",
		NoMatchReqRewriterName: "test-rewriter-3",
		CaseOptions:            newTestCaseOpts(),
	}
}

func newTestCaseOpts() map[string]*ro.CaseOptions {

	return map[string]*ro.CaseOptions{
		"1": {
			Matches:   []string{"trickster"},
			NextRoute: "test-backend-2",
		},
		"2": {
			Matches:         []string{"proxy"},
			ReqRewriterName: "test-rewriter-4",
		},
		"3": {
			Matches:     []string{"trickstercache"},
			RedirectURL: "http://trickstercache.org",
		},
		"4": {
			Matches:         []string{"true"},
			ReqRewriterName: "test-rewriter-5",
			RedirectURL:     "http://trickstercache.org",
		},
	}
}

func newTestRules() ([]*rule, error) {

	oopts := bo.New()

	rwi := newTestRewriterInstructions()

	ropts := newTestRuleOpts()

	cl1, err := NewClient("test-backend-1", nil, testMux1, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	cl2, err := NewClient("test-backend-2", nil, testMux2, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	clients := backends.Backends{"test-backend-1": cl1, "test-backend-2": cl2}

	backendClient, err := NewClient("test-client", oopts, nil, nil, clients, nil)
	if err != nil {
		return nil, err
	}

	c := backendClient.(*Client)
	rules := make([]*rule, 0, 2)

	// This creates an OpArg test rule
	err = c.parseOptions(ropts, rwi)
	if err != nil {
		return nil, err
	}
	rules = append(rules, c.rule)

	// These modifications create a CaseArg test rule
	ropts.OperationArg = "trickster"
	err = c.parseOptions(ropts, rwi)
	if err != nil {
		return nil, err
	}
	rules = append(rules, c.rule)

	return rules, nil
}

func newTestClient() (*Client, error) {

	oopts := bo.New()

	rwi, err := rewriter.ProcessConfigs(newTestRewriterOpts())
	if err != nil {
		return nil, err
	}

	ropts := newTestRuleOpts()

	cl1, err := NewClient("test-backend-1", nil, testMux1, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	cl2, err := NewClient("test-backend-2", nil, testMux2, nil, nil, nil)
	if err != nil {
		return nil, err
	}

	clients := backends.Backends{"test-backend-1": cl1, "test-backend-2": cl2}

	backendClient, err := NewClient("test-client", oopts, nil, nil, clients, nil)
	if err != nil {
		return nil, err
	}
	c := backendClient.(*Client)
	// This creates an OpArg test rule
	err = c.parseOptions(ropts, rwi)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func TestEvaluateOpArg(t *testing.T) {

	rules, err := newTestRules()
	if err != nil {
		t.Error(err)
	}

	r := rules[1]

	hr, _ := http.NewRequest(http.MethodGet, "http://www.google.com/", nil)
	hr.Header = http.Header{testRuleHeader: []string{"trickster"}}
	ctx := tc.WithHops(context.Background(), 0, 20)
	hr = hr.WithContext(ctx)

	_, _, err = r.EvaluateOpArg(hr)
	if err != nil {
		t.Error(err)
	}

	et := "test-rewriter-1, test-rewriter-5, test-rewriter-2"

	if hr.Header.Get("Test-Trail") != et {
		t.Errorf("expected %s got %s", et, hr.Header.Get("Test-Trail"))
	}

	// hr.Header.Set(testRuleHeader, "trickstercache")
	// _, _, err = r.EvaluateOpArg(hr)
	// if err != nil {
	// 	t.Error(err)
	// }

	// Make sure redirection handlers are covered
	r.defaultRedirectCode = 302
	r.defaultRedirectURL = "http://trickstercache.org"

	hr.Header.Del(testRuleHeader)
	_, _, err = r.EvaluateOpArg(hr)
	if err != nil {
		t.Error(err)
	}

	// on the final try, exceed the hop number so we can test the abort sequence

	hr = hr.WithContext(tc.WithHops(ctx, 20, 10))
	h, _, err := r.EvaluateOpArg(hr)
	if err != nil {
		t.Error(err)
	}
	if h == nil {
		t.Error("unexpected handler value")
	}

}

func TestEvaluateCaseArg(t *testing.T) {

	rules, err := newTestRules()
	if err != nil {
		t.Error(err)
	}

	r := rules[0]

	hr, _ := http.NewRequest(http.MethodGet, "http://www.google.com/", nil)
	hr.Header = http.Header{testRuleHeader: []string{"proxy"}}
	ctx := tc.WithHops(context.Background(), 0, 20)
	hr = hr.WithContext(ctx)

	_, _, err = r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}

	hr.Header.Set(testRuleHeader, "trickstercache")
	_, _, err = r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}

	// Make sure redirection handlers are covered
	r.defaultRedirectCode = 302
	r.defaultRedirectURL = "http://trickstercache.org"

	hr.Header.Del(testRuleHeader)
	_, _, err = r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}

	// on the final try, exceed the hop number so we can test the abort sequence

	hr = hr.WithContext(tc.WithHops(ctx, 20, 10))
	h, _, err := r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}
	if h == nil {
		t.Error("unexpected handler value")
	}

}
