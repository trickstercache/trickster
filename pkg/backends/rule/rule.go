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
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/redirect"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
)

const (
	trueValue  = "true"
	falseValue = "false"
)

// handleMatchedCase processes a matched case by setting the router, executing rewriters, and handling redirects
func handleMatchedCase(c *ruleCase, hr *http.Request) (http.Handler, *http.Request) {
	h := c.router

	// if this case includes rewriter instructions, execute those now
	if len(c.rewriter) > 0 {
		c.rewriter.Execute(hr)
	}

	// if it's a redirect response, set the appropriate context
	if c.redirectCode > 0 {
		hr = hr.WithContext(redirect.WithRedirects(hr.Context(),
			c.redirectCode, c.redirectURL))
	}

	return h, hr
}

type rule struct {
	defaultRouter  http.Handler
	extractionFunc extractionFunc
	operationFunc  operationFunc
	evaluatorFunc  evaluatorFunc
	negateOpResult bool

	cases caseList

	extractionArg string
	operationArg  string

	defaultRedirectURL  string
	defaultRedirectCode int
	defaultRewriter     rewriter.RewriteInstructions

	ingressReqRewriter rewriter.RewriteInstructions
	egressReqRewriter  rewriter.RewriteInstructions

	maxRuleExecutions int32
}

type ruleCase struct {
	matchValue   string
	router       http.Handler
	redirectURL  string
	redirectCode int
	rewriter     rewriter.RewriteInstructions
}

type caseList []*ruleCase

type evaluatorFunc func(*http.Request) (http.Handler, *http.Request, error)

var badRequestHandler = http.HandlerFunc(failures.HandleBadRequestResponse)

func (r *rule) EvaluateOpArg(hr *http.Request) (http.Handler, *http.Request, error) {
	currentHops, maxHops := context.Hops(hr.Context())
	if r.maxRuleExecutions < maxHops {
		maxHops = r.maxRuleExecutions
	}

	if currentHops >= maxHops {
		return badRequestHandler, hr, nil
	}

	// if this case includes ingress rewriter instructions, execute those now
	if len(r.ingressReqRewriter) > 0 {
		r.ingressReqRewriter.Execute(hr)
	}

	h := r.defaultRouter
	res := r.operationFunc(r.extractionFunc(hr, r.extractionArg),
		r.operationArg, r.negateOpResult)
	var nonDefault bool

	for _, c := range r.cases {
		if c.matchValue == res {
			nonDefault = true
			h, hr = handleMatchedCase(c, hr)
		}
	}

	if !nonDefault && r.defaultRewriter != nil {
		r.defaultRewriter.Execute(hr)
	}

	// if this case includes egress rewriter instructions, execute those now
	if len(r.egressReqRewriter) > 0 {
		r.egressReqRewriter.Execute(hr)
	}

	if !nonDefault && r.defaultRedirectCode > 0 {
		hr = hr.WithContext(redirect.WithRedirects(hr.Context(),
			r.defaultRedirectCode, r.defaultRedirectURL))
	}

	hr = hr.WithContext(context.WithHops(hr.Context(), currentHops+1, maxHops))

	return h, hr, nil
}

func (r *rule) EvaluateCaseArg(hr *http.Request) (http.Handler, *http.Request, error) {
	currentHops, maxHops := context.Hops(hr.Context())
	if r.maxRuleExecutions < maxHops {
		maxHops = r.maxRuleExecutions
	}

	if currentHops >= maxHops {
		return http.HandlerFunc(failures.HandleBadRequestResponse), hr, nil
	}

	// if this case includes ingress rewriter instructions, execute those now
	if len(r.ingressReqRewriter) > 0 {
		r.ingressReqRewriter.Execute(hr)
	}

	h := r.defaultRouter
	var nonDefault bool

	for _, c := range r.cases {
		extraction := r.extractionFunc(hr, r.extractionArg)

		res := r.operationFunc(extraction, c.matchValue, r.negateOpResult)

		// TODO: support comparison of other values via 'where'
		if res == trueValue {
			nonDefault = true
			h, hr = handleMatchedCase(c, hr)
		}
	}

	if !nonDefault && r.defaultRewriter != nil {
		r.defaultRewriter.Execute(hr)
	}

	// if this case includes egress rewriter instructions, execute those now
	if len(r.egressReqRewriter) > 0 {
		r.egressReqRewriter.Execute(hr)
	}

	if !nonDefault && r.defaultRedirectCode > 0 {
		hr = hr.WithContext(redirect.WithRedirects(hr.Context(),
			r.defaultRedirectCode, r.defaultRedirectURL))
	}

	hr = hr.WithContext(context.WithHops(hr.Context(), currentHops+1, maxHops))

	return h, hr, nil
}
