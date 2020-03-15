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
	"net/http"

	"github.com/Comcast/trickster/internal/proxy/handlers"
	"github.com/Comcast/trickster/internal/proxy/request/rewriter"
)

type rule struct {
	defaultRouter  http.Handler
	extractionFunc extractionFunc
	decodingFunc   decodingFunc
	operationFunc  operationFunc
	evaluatorFunc  evaluatorFunc
	negateOpResult bool

	cases    caseMap
	caseList caseList

	extractionArg string
	operationArg  string

	defaultRedirectURL  string
	defaultRedirectCode int

	ingressRewriter rewriter.RewriteInstructions
	egressRewriter  rewriter.RewriteInstructions
}

type ruleCase struct {
	matchValue   string
	router       http.Handler
	redirectURL  string
	redirectCode int
	rewriter     rewriter.RewriteInstructions
}

type caseMap map[string]*ruleCase
type caseList []*ruleCase

type evaluatorFunc func(*http.Request) (http.Handler, *http.Request, error)

func (r *rule) EvaluateOpArg(hr *http.Request) (http.Handler, *http.Request, error) {

	// if this case includes ingress rewriter instructions, execute those now
	if len(r.ingressRewriter) > 0 {
		r.ingressRewriter.Execute(hr)
	}

	var h http.Handler = r.defaultRouter
	res := r.operationFunc(r.extractionFunc(hr, r.extractionArg),
		r.operationArg, r.negateOpResult)
	var nonDefault bool

	if c, ok := r.cases[res]; ok {
		nonDefault = true
		h = c.router

		// if this case includes rewriter instructions, execute those now
		if len(c.rewriter) > 0 {
			c.rewriter.Execute(hr)
		}

		// if it's a redirect response, set the appropriate context
		if c.redirectCode > 0 {
			hr.WithContext(handlers.WithRedirects(hr.Context(),
				c.redirectCode, c.redirectURL))
		}
	}

	// if this case includes egress rewriter instructions, execute those now
	if len(r.egressRewriter) > 0 {
		r.egressRewriter.Execute(hr)
	}

	if !nonDefault && r.defaultRedirectCode > 0 {
		hr = hr.WithContext(handlers.WithRedirects(hr.Context(),
			r.defaultRedirectCode, r.defaultRedirectURL))
	}

	return h, hr, nil
}

func (r *rule) EvaluateCaseArg(hr *http.Request) (http.Handler, *http.Request, error) {

	// if this case includes ingress rewriter instructions, execute those now
	if len(r.ingressRewriter) > 0 {
		r.ingressRewriter.Execute(hr)
	}

	var h http.Handler = r.defaultRouter
	var nonDefault bool

	for _, c := range r.caseList {
		res := r.operationFunc(r.extractionFunc(hr, r.extractionArg),
			c.matchValue, r.negateOpResult)
		if res == "true" {
			nonDefault = true
			h = c.router

			// if this case includes rewriter instructions, execute those now
			if len(c.rewriter) > 0 {
				c.rewriter.Execute(hr)
			}

			// if it's a redirect response, set the appropriate context
			if c.redirectCode > 0 {
				hr = hr.WithContext(handlers.WithRedirects(hr.Context(),
					c.redirectCode, c.redirectURL))
			}
		}
	}

	// if this case includes egress rewriter instructions, execute those now
	if len(r.egressRewriter) > 0 {
		r.egressRewriter.Execute(hr)
	}

	if !nonDefault && r.defaultRedirectCode > 0 {
		hr = hr.WithContext(handlers.WithRedirects(hr.Context(),
			r.defaultRedirectCode, r.defaultRedirectURL))
	}

	return h, hr, nil
}
