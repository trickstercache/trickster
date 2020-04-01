/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	tc "github.com/Comcast/trickster/internal/proxy/context"
	"github.com/Comcast/trickster/internal/proxy/origins"
	oo "github.com/Comcast/trickster/internal/proxy/origins/options"
	ro "github.com/Comcast/trickster/internal/proxy/origins/rule/options"
	"github.com/Comcast/trickster/internal/proxy/request/rewriter"
	rwo "github.com/Comcast/trickster/internal/proxy/request/rewriter/options"

	"testing"
)

var testMux1 = http.NewServeMux()
var testMux2 = http.NewServeMux()

var testRuleHeader = "Test-Rule-Header"

func newTestRewriterOpts() map[string]*rwo.Options {

	return map[string]*rwo.Options{
		"test-rewriter-1": &rwo.Options{
			Instructions: [][]string{
				[]string{
					"header", "set", "Test", "test-rewriter-1",
				},
				[]string{
					"header", "append", "Test-Trail", "test-rewriter-1",
				},
			},
		},
		"test-rewriter-2": &rwo.Options{
			Instructions: [][]string{
				[]string{
					"header", "set", "Test", "test-rewriter-2",
				},
				[]string{
					"header", "append", "Test-Trail", "test-rewriter-2",
				},
			},
		},
		"test-rewriter-3": &rwo.Options{
			Instructions: [][]string{
				[]string{
					"header", "set", "Test", "test-rewriter-3",
				},
				[]string{
					"header", "append", "Test-Trail", "test-rewriter-3",
				},
			},
		},
		"test-rewriter-4": &rwo.Options{
			Instructions: [][]string{
				[]string{
					"header", "set", "Test", "test-rewriter-4",
				},
				[]string{
					"header", "append", "Test-Trail", "test-rewriter-4",
				},
			},
		},
		"test-rewriter-5": &rwo.Options{
			Instructions: [][]string{
				[]string{
					"header", "set", "Test", "test-rewriter-5",
				},
				[]string{
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
		NextRoute:              "test-origin-1",
		IngressReqRewriterName: "test-rewriter-1",
		EgressReqRewriterName:  "test-rewriter-2",
		DefaultReqRewriterName: "test-rewriter-3",
		CaseOptions:            newTestCaseOpts(),
	}
}

func newTestCaseOpts() map[string]*ro.CaseOptions {

	return map[string]*ro.CaseOptions{
		"1": &ro.CaseOptions{
			Matches:   []string{"trickster"},
			NextRoute: "test-origin-2",
		},
		"2": &ro.CaseOptions{
			Matches:         []string{"proxy"},
			ReqRewriterName: "test-rewriter-4",
		},
		"3": &ro.CaseOptions{
			Matches:     []string{"tricksterproxy"},
			RedirectURL: "http://tricksterproxy.io",
		},
		"4": &ro.CaseOptions{
			Matches:         []string{"true"},
			ReqRewriterName: "test-rewriter-5",
			RedirectURL:     "http://tricksterproxy.io",
		},
	}
}

func newTestRules() ([]*rule, error) {

	oopts := oo.NewOptions()

	rwi := newTestRewriterInstructions()

	ropts := newTestRuleOpts()

	clients := origins.Origins{"test-origin-1": &Client{router: testMux1},
		"test-origin-2": &Client{router: testMux2}}

	c, err := NewClient("test-client", oopts, nil, clients)
	if err != nil {
		return nil, err
	}

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

	oopts := oo.NewOptions()

	rwi, err := rewriter.ProcessConfigs(newTestRewriterOpts())
	if err != nil {
		return nil, err
	}

	ropts := newTestRuleOpts()

	clients := origins.Origins{"test-origin-1": &Client{router: testMux1},
		"test-origin-2": &Client{router: testMux2}}

	c, err := NewClient("test-client", oopts, nil, clients)
	if err != nil {
		return nil, err
	}

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

	// hr.Header.Set(testRuleHeader, "tricksterproxy")
	// _, _, err = r.EvaluateOpArg(hr)
	// if err != nil {
	// 	t.Error(err)
	// }

	// Make sure redirection handlers are covered
	r.defaultRedirectCode = 302
	r.defaultRedirectURL = "http://tricksterproxy.io"

	hr.Header.Del(testRuleHeader)
	_, _, err = r.EvaluateOpArg(hr)
	if err != nil {
		t.Error(err)
	}

	// on the final try, execeed the hop number so we can test the abort sequence

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

	h, _, err := r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}

	hr.Header.Set(testRuleHeader, "tricksterproxy")
	_, _, err = r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}

	// Make sure redirection handlers are covered
	r.defaultRedirectCode = 302
	r.defaultRedirectURL = "http://tricksterproxy.io"

	hr.Header.Del(testRuleHeader)
	_, _, err = r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}

	// on the final try, execeed the hop number so we can test the abort sequence

	hr = hr.WithContext(tc.WithHops(ctx, 20, 10))
	h, _, err = r.EvaluateCaseArg(hr)
	if err != nil {
		t.Error(err)
	}
	if h == nil {
		t.Error("unexpected handler value")
	}

}
