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
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	rwo "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

func newRegexCaptureRule(t *testing.T) *rule {
	t.Helper()

	rwi, err := rewriter.ProcessConfigs(rwo.Lookup{
		"capture-host": {
			Instructions: rwo.RewriteList{
				{"hostname", "set", "${tenant}.writer.example.com"},
			},
		},
		"capture-header": {
			Instructions: rwo.RewriteList{
				{"header", "set", "X-Egress-Captures", "${1}:${tenant}:${0}:${missing}"},
			},
		},
		"no-match": {
			Instructions: rwo.RewriteList{
				{"header", "set", "X-No-Match-Capture", "${tenant}"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	destination, err := NewClient("destination", nil, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := backends.Backends{"destination": destination}
	backendClient, err := NewClient("capture-rule", bo.New(), nil, nil, clients, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := backendClient.(*Client)
	err = c.parseOptions(&ro.Options{
		Name:                   "capture-rule",
		InputType:              "string",
		InputSource:            "path",
		Operation:              "rmatch",
		OperationArg:           `\{mylabel="(?P<tenant>[a-z0-9]{3})"\}`,
		NextRoute:              "destination",
		EgressReqRewriterName:  "capture-header",
		NoMatchReqRewriterName: "no-match",
		CaseOptions: ro.CaseOptionsList{
			{
				Matches:         []string{trueValue},
				ReqRewriterName: "capture-host",
				NextRoute:       "destination",
			},
		},
	}, rwi)
	if err != nil {
		t.Fatal(err)
	}
	if !c.rule.hasCaptureTokens {
		t.Fatal("rule did not detect capture token use")
	}
	return c.rule
}

func TestRegexCaptureTokensIncludeOptionalGroups(t *testing.T) {
	re := regexp.MustCompile(`(?P<optional>foo)?(?P<tenant>bar)`)
	tokens := regexCaptureTokens(re, re.FindStringSubmatch("bar"))
	wants := map[string]string{
		"0":        "bar",
		"1":        "",
		"2":        "bar",
		"optional": "",
		"tenant":   "bar",
	}
	for key, want := range wants {
		if got := tokens[key]; got != want {
			t.Errorf("token %q = %q, want %q", key, got, want)
		}
	}
}

func TestRegexCaptureRuleDoesNotRepeatRegexEvaluation(t *testing.T) {
	rule := newRegexCaptureRule(t)
	originalOperation := rule.operationFunc
	var operationCalls int
	rule.operationFunc = func(input, arg string, negate bool) string {
		operationCalls++
		return originalOperation(input, arg, negate)
	}

	req := httptest.NewRequest(http.MethodGet,
		`/query/%7Bmylabel%3D%22abc%22%7D`, nil)
	if _, _, err := rule.EvaluateOpArg(req); err != nil {
		t.Fatal(err)
	}
	if operationCalls != 0 {
		t.Errorf("operation calls = %d; want 0", operationCalls)
	}
}

func TestRegexCapturesRewriteRequest(t *testing.T) {
	rule := newRegexCaptureRule(t)
	req := httptest.NewRequest(http.MethodGet,
		`/query/%7Bmylabel%3D%22abc%22%7D`, nil)
	if req.URL.Host != "" {
		t.Fatalf("inbound URL host = %q, want empty", req.URL.Host)
	}

	_, rewritten, err := rule.EvaluateOpArg(req)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rewritten.URL.Hostname(), "abc.writer.example.com"; got != want {
		t.Fatalf("hostname = %q, want %q", got, want)
	}
	if got, want := rewritten.Header.Get("X-Egress-Captures"),
		`abc:abc:{mylabel="abc"}:${missing}`; got != want {
		t.Fatalf("X-Egress-Captures = %q, want %q", got, want)
	}
	base, err := url.Parse("http://writer.example.com:9090/base")
	if err != nil {
		t.Fatal(err)
	}
	upstream := urls.BuildUpstreamURL(rewritten, base)
	if got, want := upstream.Host, "abc.writer.example.com:9090"; got != want {
		t.Fatalf("upstream host = %q, want %q", got, want)
	}

	downstream, err := rewriter.ParseRewriteList(rwo.RewriteList{
		{"header", "set", "X-Downstream-Capture", "${tenant}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	downstream.Execute(rewritten)
	if got, want := rewritten.Header.Get("X-Downstream-Capture"), "${tenant}"; got != want {
		t.Fatalf("downstream capture = %q, want %q", got, want)
	}
}

func TestRegexCaptureRewritesFinalUpstreamRequest(t *testing.T) {
	type upstreamRequest struct {
		host string
		path string
	}
	requests := make(chan upstreamRequest, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- upstreamRequest{host: r.Host, path: r.URL.Path}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, originURL.Host)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)

	rule := newRegexCaptureRule(t)
	req, err := http.NewRequest(http.MethodGet,
		`http://trickster.example/query/%7Bmylabel%3D%22abc%22%7D`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, req, err = rule.EvaluateOpArg(req)
	if err != nil {
		t.Fatal(err)
	}

	backendOptions := bo.New()
	backendOptions.Name = "writer"
	backendOptions.HTTPClient = &http.Client{Transport: transport}
	pathOptions := po.New()
	req = request.SetResources(req,
		request.NewResources(backendOptions, pathOptions, nil, nil, nil, nil))
	base, err := url.Parse("http://writer.example.com:9090/base")
	if err != nil {
		t.Fatal(err)
	}
	req.URL = urls.BuildUpstreamURL(req, base)

	reader, resp, _ := engines.PrepareFetchReader(req)
	if reader != nil {
		defer reader.Close()
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	upstream := <-requests
	if got, want := upstream.host, "abc.writer.example.com:9090"; got != want {
		t.Errorf("origin Host = %q, want %q", got, want)
	}
	if got, want := upstream.path, `/base/query/{mylabel="abc"}`; got != want {
		t.Errorf("origin path = %q, want %q", got, want)
	}
}

func TestRegexCaptureTokensDoNotLeakBetweenRequests(t *testing.T) {
	rule := newRegexCaptureRule(t)
	results := make(chan error, 100)

	for i := range 100 {
		go func() {
			tenant := fmt.Sprintf("a%02d", i)
			req, err := http.NewRequest(http.MethodGet,
				fmt.Sprintf(`http://trickster.example/query/%%7Bmylabel%%3D%%22%s%%22%%7D`, tenant), nil)
			if err != nil {
				results <- err
				return
			}
			_, rewritten, err := rule.EvaluateOpArg(req)
			if err != nil {
				results <- err
				return
			}
			if got, want := rewritten.URL.Hostname(), tenant+".writer.example.com"; got != want {
				results <- fmt.Errorf("hostname = %q, want %q", got, want)
				return
			}
			results <- nil
		}()
	}

	for range 100 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

func TestRegexCaptureTokensAreClearedOnNoMatch(t *testing.T) {
	rule := newRegexCaptureRule(t)
	req, err := http.NewRequest(http.MethodGet,
		`http://trickster.example/query/%7Bmylabel%3D%22abc%22%7D`, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, rewritten, err := rule.EvaluateOpArg(req)
	if err != nil {
		t.Fatal(err)
	}
	rewritten.URL.Path = "/query/no-label"
	_, rewritten, err = rule.EvaluateOpArg(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rewritten.Header.Get("X-No-Match-Capture"), "${tenant}"; got != want {
		t.Fatalf("X-No-Match-Capture = %q, want %q", got, want)
	}
	if got, want := rewritten.Header.Get("X-Egress-Captures"),
		"${1}:${tenant}:${0}:${missing}"; got != want {
		t.Fatalf("X-Egress-Captures = %q, want %q", got, want)
	}
}

func TestRegexCapturesAreUnavailableWithoutMatchingCase(t *testing.T) {
	rule := newRegexCaptureRule(t)
	rule.cases[0].matchValue = falseValue
	req, err := http.NewRequest(http.MethodGet,
		`http://trickster.example/query/%7Bmylabel%3D%22abc%22%7D`, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, rewritten, err := rule.EvaluateOpArg(req)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rewritten.Header.Get("X-No-Match-Capture"), "${tenant}"; got != want {
		t.Fatalf("X-No-Match-Capture = %q, want %q", got, want)
	}
	if got, want := rewritten.Header.Get("X-Egress-Captures"),
		"${1}:${tenant}:${0}:${missing}"; got != want {
		t.Fatalf("X-Egress-Captures = %q, want %q", got, want)
	}
}
