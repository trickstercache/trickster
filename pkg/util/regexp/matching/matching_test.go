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

package matching

import (
	"regexp"
	"testing"
)

var testRegexp *regexp.Regexp

func init() {
	testRegexp = regexp.MustCompile(`(?P<value>test)`)
}

func TestGetNamedMatches(t *testing.T) {

	m := GetNamedMatches(testRegexp, "i love tests!", nil)
	if len(m) != 1 {
		t.Errorf("expected %d got %d", 1, len(m))
	}

	m = GetNamedMatches(testRegexp, "", nil)
	if len(m) != 0 {
		t.Errorf("expected %d got %d", 0, len(m))
	}

	m = GetNamedMatches(testRegexp, "a", nil)
	if len(m) != 0 {
		t.Errorf("expected %d got %d", 0, len(m))
	}

	m = GetNamedMatches(testRegexp, "i love tests!", []string{"value"})
	if len(m) != 1 {
		t.Errorf("expected %d got %d", 1, len(m))
	}

}

func TestGetNamedMatch(t *testing.T) {

	s, b := GetNamedMatch("", testRegexp, "i love tests!")
	if b {
		t.Errorf("expected %t got %t", false, b)
	}
	if s != "" {
		t.Errorf("expected %s got %s", "", s)
	}

	s, b = GetNamedMatch("value", testRegexp, "i love tests!")
	if !b {
		t.Errorf("expected %t got %t", true, b)
	}
	if s != "test" {
		t.Errorf("expected %s got %s", "test", s)
	}

	s, b = GetNamedMatch("a", testRegexp, "i love tests!")
	if b {
		t.Errorf("expected %t got %t", false, b)
	}
	if s != "" {
		t.Errorf("expected %s got %s", "", s)
	}

	s, b = GetNamedMatch("value", testRegexp, "a")
	if b {
		t.Errorf("expected %t got %t", false, b)
	}
	if s != "" {
		t.Errorf("expected %s got %s", "", s)
	}

}
