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

	"github.com/gorilla/mux"
)

type rule struct {
	defaultRouter  *mux.Router
	extractionFunc extractionFunc
	decodingFunc   decodingFunc
	operationFunc  operationFunc
	evaluatorFunc  evaluatorFunc
	negateOpResult bool

	cases    caseMap
	caseList caseList

	extractionArg string
	operationArg  string
}

type ruleCase struct {
	matchValue string
	router     *mux.Router
}

type caseMap map[string]*ruleCase
type caseList []*ruleCase

type evaluatorFunc func(*http.Request) (http.Handler, error)

func (r *rule) EvaluateOpArg(hr *http.Request) (http.Handler, error) {

	// TODO: if pre-evaluation rewrite, do it here.

	h := r.defaultRouter
	result := r.operationFunc(r.extractionFunc(hr, r.extractionArg), r.operationArg, r.negateOpResult)
	if rh, ok := r.cases[result]; ok {
		h = rh.router
		// TODO: if case-based rewrite, do it here.
	}

	// TODO: if post-evaluation rewrite, do it here.

	return h, nil
}

func (r *rule) EvaluateCaseArg(hr *http.Request) (http.Handler, error) {

	// TODO: if pre-evaluation rewrite, do it here.

	h := r.defaultRouter
	for _, c := range r.caseList {
		result := r.operationFunc(r.extractionFunc(hr, r.extractionArg), c.matchValue, r.negateOpResult)
		if result == "true" {
			h = c.router
			// TODO: if case-based rewrite, do it here.
		}
	}

	// TODO: if post-evaluation rewrite, do it here.

	return h, nil
}
