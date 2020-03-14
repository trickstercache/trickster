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
}

type ruleCase struct {
	matchValue   string
	router       http.Handler
	redirectURL  string
	redirectCode int
}

type caseMap map[string]*ruleCase
type caseList []*ruleCase

type evaluatorFunc func(*http.Request) (http.Handler, *http.Request, error)

func (r *rule) EvaluateOpArg(hr *http.Request) (http.Handler, *http.Request, error) {

	// TODO: if pre-evaluation rewrite, do it here.

	var h http.Handler = r.defaultRouter
	res := r.operationFunc(r.extractionFunc(hr, r.extractionArg),
		r.operationArg, r.negateOpResult)
	var nonDefault bool

	if c, ok := r.cases[res]; ok {
		nonDefault = true
		h = c.router

		// if it's a redirect response, set the appropriate context
		if c.redirectCode > 0 {
			hr.WithContext(handlers.WithRedirects(hr.Context(),
				c.redirectCode, c.redirectURL))
		}

		// TODO: if case-based rewrite, do it here.
	}

	// TODO: if post-evaluation rewrite, do it here.

	if !nonDefault && r.defaultRedirectCode > 0 {
		hr = hr.WithContext(handlers.WithRedirects(hr.Context(),
			r.defaultRedirectCode, r.defaultRedirectURL))
	}

	return h, hr, nil
}

func (r *rule) EvaluateCaseArg(hr *http.Request) (http.Handler, *http.Request, error) {

	// TODO: if pre-evaluation rewrite, do it here.

	var h http.Handler = r.defaultRouter
	var nonDefault bool

	for _, c := range r.caseList {
		res := r.operationFunc(r.extractionFunc(hr, r.extractionArg),
			c.matchValue, r.negateOpResult)
		if res == "true" {
			nonDefault = true
			h = c.router

			// if it's a redirect response, set the appropriate context
			if c.redirectCode > 0 {
				hr = hr.WithContext(handlers.WithRedirects(hr.Context(),
					c.redirectCode, c.redirectURL))
			}

			// TODO: if case-based rewrite, do it here.
		}
	}

	// TODO: if post-evaluation rewrite, do it here.

	if !nonDefault && r.defaultRedirectCode > 0 {
		hr = hr.WithContext(handlers.WithRedirects(hr.Context(),
			r.defaultRedirectCode, r.defaultRedirectURL))
	}

	return h, hr, nil
}
