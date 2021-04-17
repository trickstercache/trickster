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
	"strconv"
	"testing"
)

func TestBToS(t *testing.T) {

	b := btos(true, false)
	expected := "true"
	if b != expected {
		t.Errorf("expected %s got %s", expected, b)
	}

	b = btos(false, true)
	expected = "true"
	if b != expected {
		t.Errorf("expected %s got %s", expected, b)
	}

	b = btos(false, false)
	expected = "false"
	if b != expected {
		t.Errorf("expected %s got %s", expected, b)
	}

}

func TestOperations(t *testing.T) {

	tests := []struct {
		opKey, input, arg string
		negate            bool
		expected          string
	}{
		{"string-eq", "test", "test", false, "true"},
		{"string-rmatch", "/example/writer", "^.*\\/writer.*$", false, "true"},
		{"string-rmatch", "mytesting", "^.*[test.*$", false, "false"},
		{"string-rmatch", "mytesting", "^.*tst.*$", false, "false"},
		{"string-contains", "test", "test", false, "true"},
		{"string-contains", "test", "foo", false, "false"},
		{"string-prefix", "test", "t", false, "true"},
		{"string-prefix", "test", "e", false, "false"},
		{"string-suffix", "test", "t", false, "true"},
		{"string-suffix", "test", "e", false, "false"},
		{"string-md5", "test", "", false, "098f6bcd4621d373cade4e832627b4f6"},
		{"string-sha1", "test", "", false, "a94a8fe5ccb19ba61c4c0873d391e987982fbbd3"},
		{"string-base64", "test", "", false, "dGVzdA=="},
		{"string-modulo", "trickster", "7", false, "0"},
		{"string-modulo", "trickster", "a", false, ""},
		{"num-eq", "1", "1", false, "true"},
		{"num-eq", "1", "", false, ""},
		{"num-gt", "1", "1", false, "false"},
		{"num-gt", "1", "", false, ""},
		{"num-lt", "1", "2", false, "true"},
		{"num-lt", "1", "", false, ""},
		{"num-lt", "", "1", false, ""},
		{"num-ge", "1", "2", false, "false"},
		{"num-ge", "1", "1", false, "true"},
		{"num-ge", "2", "1", false, "true"},
		{"num-ge", "1", "", false, ""},
		{"num-le", "1", "2", false, "true"},
		{"num-le", "1", "1", false, "true"},
		{"num-le", "2", "1", false, "false"},
		{"num-le", "1", "", false, ""},
		{"num-bt", "1", "", false, ""},
		{"num-bt", "1", "0-5", false, "true"},
		{"num-bt", "0", "0-5", false, "true"},
		{"num-bt", "-1", "0-5", false, "false"},
		{"num-bt", "6", "0-5", false, "false"},
		{"num-bt", "", "0-5", false, ""},
		{"num-bt", "6", "foo-5", false, ""},
		{"num-modulo", "6", "4", false, "2"},
		{"num-modulo", "6", "6", false, "0"},
		{"num-modulo", "a", "4", false, ""},
		{"num-modulo", "4", "a", false, ""},
		{"bool-eq", "true", "true", false, "true"},
		{"bool-eq", "a", "true", false, ""},
		{"bool-eq", "true", "a", false, ""},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if f, ok := operationFuncs[operation(test.opKey)]; ok {
				got := f(test.input, test.arg, test.negate)
				if got != test.expected {
					t.Errorf("input: %s, args: %s \ngot      %s\nexpected %s", test.input, test.arg, got, test.expected)
				}
			} else {
				t.Errorf("unknown operation %v", test.opKey)
			}
		})
	}
}
