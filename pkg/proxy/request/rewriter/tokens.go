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
	"context"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type tokenContextKey struct{}

// WithTokens returns a shallow copy of r carrying request-scoped rewrite tokens.
func WithTokens(r *http.Request, tokens map[string]string) *http.Request {
	if r == nil {
		return nil
	}
	ctx := context.WithValue(r.Context(), tokenContextKey{}, maps.Clone(tokens))
	return r.WithContext(ctx)
}

// WithoutTokens returns a shallow copy of r without rewrite tokens.
func WithoutTokens(r *http.Request) *http.Request {
	if len(tokensFromRequest(r)) == 0 {
		return r
	}
	return WithTokens(r, nil)
}

func tokensFromRequest(r *http.Request) map[string]string {
	if r == nil {
		return nil
	}
	tokens, _ := r.Context().Value(tokenContextKey{}).(map[string]string)
	return tokens
}

func expandTokens(r *http.Request, input string) string {
	tokens := tokensFromRequest(r)
	if len(tokens) == 0 || !checkTokens(input) {
		return input
	}

	var output strings.Builder
	for len(input) > 0 {
		start := strings.Index(input, "${")
		if start < 0 {
			output.WriteString(input)
			break
		}
		output.WriteString(input[:start])
		end := strings.IndexByte(input[start+2:], '}')
		if end < 0 {
			output.WriteString(input[start:])
			break
		}
		end += start + 2
		name := input[start+2 : end]
		if value, ok := tokens[name]; ok {
			output.WriteString(value)
		} else {
			output.WriteString(input[start : end+1])
		}
		input = input[end+1:]
	}
	return output.String()
}

// CaptureTokens maps regexp submatches to rewrite tokens: ${0} for the whole
// match, ${n} for each group, and ${name} for named groups.
func CaptureTokens(re *regexp.Regexp, matches []string) map[string]string {
	if len(matches) == 0 {
		return nil
	}
	tokens := make(map[string]string, len(matches)*2)
	names := re.SubexpNames()
	for i, match := range matches {
		tokens[strconv.Itoa(i)] = match
		if i >= len(names) || names[i] == "" {
			continue
		}
		if _, ok := tokens[names[i]]; !ok {
			tokens[names[i]] = match
		}
	}
	return tokens
}

// WithPathCaptures wraps next so each request carries the submatches of re
// against its path as rewrite tokens. Attach only to routes whose rewriters
// use tokens, since the submatch runs on every request through the wrapper.
func WithPathCaptures(re *regexp.Regexp, next http.Handler) http.Handler {
	if re == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokens := CaptureTokens(re, re.FindStringSubmatch(r.URL.Path)); len(tokens) > 0 {
			r = WithTokens(r, tokens)
		}
		next.ServeHTTP(w, r)
	})
}
