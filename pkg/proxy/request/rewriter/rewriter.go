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
	"errors"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

var errInvalidRewriterOptions = errors.New("invalid rewriter options")

// ProcessConfigs validates and compiles rewriter instructions from
// the provided configuration map
func ProcessConfigs(rwl map[string]*options.Options) (map[string]RewriteInstructions, error) {
	if rwl == nil {
		return nil, errInvalidRewriterOptions
	}

	crw := make(map[string]RewriteInstructions)
	for k, v := range rwl {
		ri, err := ParseRewriteList(v.Instructions)
		if err != nil {
			return nil, err
		}
		crw[k] = ri
	}

	// this validates the rewriter names in the rewriter chains
	for _, ri := range crw {
		for _, instr := range ri {
			if ce, ok := instr.(*rwiChainExecutor); ok {
				rwi, ok := crw[ce.rewriterName]
				if !ok {
					return nil, errInvalidRewriterOptions
				}
				ce.rewriter = rwi
			}
		}
	}

	return crw, nil
}

// ParseRewriteList converts a Rewriter Configuration into parsed instructions
func ParseRewriteList(rl options.RewriteList) (RewriteInstructions, error) {
	fri := make(RewriteInstructions, 0, len(rl))
	for _, sri := range rl {
		if len(sri) > 1 {
			key := sri[0] + "-" + sri[1]
			f, ok := rewriters[key]
			if !ok {
				return nil, errBadParams
			}
			ri := f()
			err := ri.Parse(sri)
			if err != nil {
				return nil, err
			}
			fri = append(fri, ri)
		}
	}
	return fri, nil
}

// Rewrite returns a handler that executes the Rewriter and passes
// the request to the next Handler
func Rewrite(ri RewriteInstructions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri.Execute(r)
		next.ServeHTTP(w, r)
	})
}
