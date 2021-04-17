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
	"strings"
	"testing"
)

func TestParseOptions(t *testing.T) {

	c, err := newTestClient()
	if err != nil {
		t.Error(err)
	}

	err = c.parseOptions(nil, nil)
	expected := "failed to parse nil options"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	ropts := newTestRuleOpts()
	rwi := newTestRewriterInstructions()

	temp := ropts.InputSource
	ropts.InputSource = ""
	err = c.parseOptions(ropts, rwi)
	ropts.InputSource = temp
	expected = "options missing input_source"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.InputType
	ropts.InputType = ""
	err = c.parseOptions(ropts, rwi)
	ropts.InputType = temp
	expected = "options missing input_type"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.Operation
	ropts.Operation = ""
	err = c.parseOptions(ropts, rwi)
	ropts.Operation = temp
	expected = "options missing operation"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.EgressReqRewriterName
	ropts.EgressReqRewriterName = "x"
	err = c.parseOptions(ropts, rwi)
	ropts.EgressReqRewriterName = temp
	expected = "invalid egress rewriter"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.IngressReqRewriterName
	ropts.IngressReqRewriterName = "x"
	err = c.parseOptions(ropts, rwi)
	ropts.IngressReqRewriterName = temp
	expected = "invalid ingress rewriter"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.NoMatchReqRewriterName
	ropts.NoMatchReqRewriterName = "x"
	err = c.parseOptions(ropts, rwi)
	ropts.NoMatchReqRewriterName = temp
	expected = "invalid default rewriter"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.NextRoute
	ropts.NextRoute = "x"
	err = c.parseOptions(ropts, rwi)
	ropts.NextRoute = temp
	expected = "invalid default rule route"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	expected = "x"
	temp = ropts.RedirectURL
	temp2 := ropts.NextRoute
	ropts.NextRoute = ""
	ropts.RedirectURL = expected
	err = c.parseOptions(ropts, rwi)
	ropts.RedirectURL = temp
	ropts.NextRoute = temp2
	if err != nil {
		t.Error(err)
	} else if c.rule.defaultRedirectURL != expected {
		t.Errorf("expected %s for %s", expected, c.rule.defaultRedirectURL)
	}

	temp = ropts.RedirectURL
	temp2 = ropts.NextRoute
	ropts.NextRoute = ""
	ropts.RedirectURL = ""
	err = c.parseOptions(ropts, rwi)
	ropts.RedirectURL = temp
	ropts.NextRoute = temp2
	expected = "invalid default rule route"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.InputSource
	ropts.InputSource = "something-invalid"
	err = c.parseOptions(ropts, rwi)
	ropts.InputSource = temp
	expected = "invalid source name"
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.InputDelimiter
	temp3 := ropts.InputIndex
	ropts.InputIndex = 1
	ropts.InputDelimiter = ","
	err = c.parseOptions(ropts, rwi)
	ropts.InputDelimiter = temp
	ropts.InputIndex = temp3
	if err != nil {
		t.Error(err)
	} else if c.rule.extractionFunc == nil {
		t.Error("expected non-nil extractionFunc")
	}
	c.rule.extractionFunc(nil, "")

	temp = ropts.InputEncoding
	ropts.InputEncoding = "base64"
	err = c.parseOptions(ropts, rwi)
	ropts.InputEncoding = temp
	if err != nil {
		t.Error(err)
	} else if c.rule.extractionFunc == nil {
		t.Error("expected non-nil extractionFunc")
	}
	c.rule.extractionFunc(nil, "")

	expected = "invalid encoding name"
	temp = ropts.InputEncoding
	ropts.InputEncoding = "invalid"
	err = c.parseOptions(ropts, rwi)
	ropts.InputEncoding = temp
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	temp = ropts.Operation
	ropts.Operation = "!eq"
	err = c.parseOptions(ropts, rwi)
	ropts.Operation = temp
	if err != nil {
		t.Error(err)
	} else if !c.rule.negateOpResult {
		t.Error("expected true got false")
	}

	expected = "invalid operation"
	temp = ropts.Operation
	ropts.Operation = "invalid"
	err = c.parseOptions(ropts, rwi)
	ropts.Operation = temp
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	expected = "invalid rewriter"
	temp = ropts.CaseOptions["1"].ReqRewriterName
	ropts.CaseOptions["1"].ReqRewriterName = "invalid"
	err = c.parseOptions(ropts, rwi)
	ropts.CaseOptions["1"].ReqRewriterName = temp
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	expected = "missing next_route"
	temp = ropts.CaseOptions["1"].ReqRewriterName
	temp2 = ropts.CaseOptions["1"].RedirectURL
	temp4 := ropts.CaseOptions["1"].NextRoute
	ropts.CaseOptions["1"].ReqRewriterName = ""
	ropts.CaseOptions["1"].RedirectURL = ""
	ropts.CaseOptions["1"].NextRoute = ""
	err = c.parseOptions(ropts, rwi)
	ropts.CaseOptions["1"].ReqRewriterName = temp
	ropts.CaseOptions["1"].RedirectURL = temp2
	ropts.CaseOptions["1"].NextRoute = temp4
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	expected = "missing matches in rule"
	temp5 := ropts.CaseOptions["1"].Matches
	ropts.CaseOptions["1"].Matches = []string{}
	err = c.parseOptions(ropts, rwi)
	ropts.CaseOptions["1"].Matches = temp5
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}

	expected = "unknown next_route"
	temp = ropts.CaseOptions["1"].NextRoute
	ropts.CaseOptions["1"].NextRoute = "invalid"
	err = c.parseOptions(ropts, rwi)
	ropts.CaseOptions["1"].NextRoute = temp
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error for %s", expected)
	}
}
