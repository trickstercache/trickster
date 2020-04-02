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

package rewriter

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter/options"
)

// ProcessConfigs validates and compiles rewriter instructions from
// the provided configuration map
func ProcessConfigs(rwl map[string]*options.Options) (map[string]RewriteInstructions, error) {
	crw := make(map[string]RewriteInstructions)
	for k, v := range rwl {
		ri, err := parseRewriteList(v.Instructions)
		if err != nil {
			return nil, err
		}
		crw[k] = ri
	}
	return crw, nil
}

func parseRewriteList(rl options.RewriteList) (RewriteInstructions, error) {
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
