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

// Package matching provides patterns for processing regexp.Regexp matches
package matching

import "regexp"

// GetNamedMatches will return a map of NamedSubmatches=Value for a Regexp and input string,
// filtered to the provided list when populated. If there are multiple matches of the same name, last one wins
func GetNamedMatches(re *regexp.Regexp, input string, filter []string) map[string]string {

	found := make(map[string]string)
	if input == "" || re == nil {
		return found
	}

	matches := re.FindStringSubmatch(input)
	if len(matches) == 0 {
		return found
	}

	have := re.SubexpNames()
	if len(have) == 0 {
		return found
	}

	have = have[1:]
	matches = matches[1:]
	useFilter := len(filter) > 0

	// Go through the matches
	for i, n := range matches {
		if useFilter {
			for _, name := range filter {
				if name != "" && n != "" && have[i] == name {
					found[name] = n
				}
			}
		} else if have[i] != "" && n != "" {
			found[have[i]] = n
		}
	}
	return found
}

// GetNamedMatch will return the value of a Named Submatch for a given regexp and its matches.
// If there are multiple matches of the same name, first one wins
func GetNamedMatch(filter string, re *regexp.Regexp, input string) (string, bool) {

	if input == "" || filter == "" || re == nil {
		return "", false
	}

	matches := re.FindStringSubmatch(input)
	if len(matches) == 0 {
		return "", false
	}

	have := re.SubexpNames()
	if len(have) == 0 {
		return "", false
	}

	have = have[1:]
	matches = matches[1:]

	// Go through the matches
	for i, n := range matches {
		if filter != "" && have[i] == filter {
			return n, true
		}
	}

	return "", false

}
